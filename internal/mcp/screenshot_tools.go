package mcp

// MCP tools for the screenshot capability:
//
//   social_fetch_screenshot       capture a PNG of any URL
//   social_fetch_read_screenshot  re-fetch a previously captured PNG
//                                  through the MCP channel (for clients
//                                  that can't reach the ledger daemon URL)
//
// Image return strategy:
//
//   small PNG (≤ inlineCap)  → returned as both:
//                                 - TextContent (envelope JSON: url, file, etc.)
//                                 - ImageContent (base64 PNG, mime image/png)
//                              Claude Desktop renders the image inline; no
//                              follow-up fetch needed. Same for cloud LLMs
//                              that can't reach 127.0.0.1.
//
//   large PNG (> inlineCap)   → only the envelope (url + file + bytes).
//                              Anthropic caps inline images at ~5 MB and
//                              base64 inflates ~33 %, so we keep our
//                              inline cutoff well under that to leave
//                              context-window headroom. Agents on the
//                              same machine read content_file; cross-
//                              machine agents call read_screenshot with
//                              a max_height to get a viewable slice.
//
// max_height (optional, applied at MCP layer): crop the captured PNG to
// the top N pixels before encoding/storing. Useful for vision-tool
// consumption — a 30,000px tall page screenshot exceeds the per-image
// cap; capping at 4096 keeps it under the limit while showing the
// page header + first scrollful.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/ledger"
	"github.com/jedi4ever/social-skills/internal/render/headless"
)

// inlineCap is the max raw PNG size we'll embed inline as
// ImageContent. Anthropic's per-image cap is 5 MB; base64 adds
// ~33 % overhead, so 3 MB raw → ~4 MB base64, comfortably under
// the limit while leaving headroom for the rest of the response
// context. The previous 1.5 MB value was over-conservative and
// silently dropped many real-world page screenshots into the
// "URL only" path that doesn't work for cloud-side agents
// (claude.ai, ChatGPT, etc.).
const inlineCap = 3_000_000

// autoCropHeight is the fallback height we crop to when a
// screenshot would otherwise exceed inlineCap. Picked at 4096 so
// the result captures a full above-the-fold + first scrollful
// view; agents who need more can paginate via
// social_fetch_read_screenshot's offset_y.
const autoCropHeight = 4096

// readScreenshotMaxHeightDefault is what the read tool uses when
// the agent doesn't specify max_height. Picked so a 1920px-wide
// PNG cropped to 4096 tall stays under the inline cap for
// realistic page densities (~150 KB/megapixel for typical web
// pages).
const readScreenshotMaxHeightDefault = 4096

type screenshotArgs struct {
	URL       string `json:"url"`
	FullPage  *bool  `json:"full_page,omitempty"`
	Settle    string `json:"settle,omitempty"`
	MaxHeight int    `json:"max_height,omitempty"`
}

func addScreenshotTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_fetch_screenshot",
		mcp.WithDescription("Capture a PNG screenshot of any URL via the headless browser. Default viewport 1920x1080, full-page capture (the whole scrollable document, not just the viewport). Goes through the same headless daemon pool as social_fetch_fetch — typical 1-2s when the daemon is warm.\n\nResponse is multi-content: (1) JSON envelope with metadata (width/height/original_height/content_file/content_url), (2) the PNG inline as image content — vision-capable clients (Claude Desktop, claude.ai, ChatGPT-with-MCP) render it natively without a follow-up fetch. (3) When the captured page is too tall to fit the inline-image cap (~3 MB raw), the response is auto-cropped to the top 4096 px and a `NOTE: this is a slice…` text block is added BEFORE the image with the exact filename + offset_y the agent should pass to social_fetch_read_screenshot to walk the rest of the page.\n\nViewing options for any client:\n  • Inline image — appears in agent context automatically. Best UX, no extra call.\n  • content_file — local PNG path. Claude Code: call its built-in Read tool on the path to render in chat. Claude Desktop: social_fetch_read_file.\n  • content_url — ledger daemon HTTP URL. Works for cross-machine / containerised setups where the agent can reach the daemon over the network.\n\nUse for: visual verification of a fetched page, capturing dynamic content, before/after UI checks, debugging anti-bot pages."),
		mcp.WithString("url", mcp.Required(), mcp.Description("URL to screenshot. Same redirect-handling as social_fetch_fetch.")),
		mcp.WithBoolean("full_page", mcp.Description("Capture the whole scrollable page (default true). Set false for viewport-only.")),
		mcp.WithString("settle", mcp.Description("JS-hydration wait after navigate, Go duration syntax (e.g. \"5s\", \"2500ms\"). Default 2s. Bump for SPAs that take longer to render after first paint.")),
		mcp.WithNumber("max_height", mcp.Description("Crop the captured PNG to the top N pixels before encoding/storing. 0 (default) = no crop. Use this when targeting vision: a 30k-pixel tall page exceeds the per-image cap; capping at e.g. 4096 keeps the result inline-renderable while showing the page header + first scrollful.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args screenshotArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "screenshot")
		defer closeAudit()
		if strings.TrimSpace(args.URL) == "" {
			return mcp.NewToolResultError("url is required"), nil
		}
		fullPage := true
		if args.FullPage != nil {
			fullPage = *args.FullPage
		}
		settle := time.Duration(0)
		if s := strings.TrimSpace(args.Settle); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("settle: %v", err)), nil
			}
			if d < 0 {
				return mcp.NewToolResultError("settle must be non-negative"), nil
			}
			settle = d
		}

		f := headless.New()
		res, err := f.Screenshot(ctx, args.URL, headless.ScreenshotOptions{
			FullPage: fullPage,
			Settle:   settle,
		})
		if err != nil {
			audit.Logf("screenshot %s FAILED: %v", args.URL, err)
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Capture the original dimensions + size BEFORE any
		// cropping so the agent can decide whether the slice
		// they're seeing is the whole page or just a window. A
		// 30 000 px tall page cropped to 4096 still tells the
		// agent there's more below at y=4096.
		origW, origH, _ := headless.PNGDims(res.PNG)
		origBytes := len(res.PNG)

		// Optional crop. Applied at the MCP layer (not the headless
		// driver) so the cap is consistent regardless of which
		// engine produced the bytes.
		pngBytes := res.PNG
		var cropped, autoCropped bool
		appliedMax := args.MaxHeight
		if args.MaxHeight > 0 {
			out, didCrop, cerr := headless.CropPNGTop(pngBytes, args.MaxHeight)
			if cerr != nil {
				// Crop failure isn't fatal — log and use original.
				audit.Logf("screenshot %s crop failed: %v", args.URL, cerr)
			} else {
				pngBytes = out
				cropped = didCrop
			}
		}
		// Auto-crop oversized screenshots when the caller didn't
		// already cap the height. Cloud-side agents (claude.ai,
		// remote MCP) only see the image via inline ImageContent;
		// silently letting a 30k-px tall page fall through to
		// "URL only" leaves them blind. Crop to autoCropHeight so
		// the inline path always carries something useful, and
		// surface auto_cropped + next_offset_y so the agent knows
		// how to walk the rest of the page if it cares.
		if !cropped && len(pngBytes) > inlineCap {
			out, didCrop, cerr := headless.CropPNGTop(pngBytes, autoCropHeight)
			if cerr != nil {
				audit.Logf("screenshot %s auto-crop failed: %v", args.URL, cerr)
			} else if didCrop {
				pngBytes = out
				cropped = true
				autoCropped = true
				appliedMax = autoCropHeight
			}
		}
		w, h, _ := headless.PNGDims(pngBytes)

		env := map[string]any{
			"url":             args.URL,
			"final_url":       res.FinalURL,
			"engine":          res.Engine,
			"full_page":       fullPage,
			"content_bytes":   len(pngBytes),
			"content_type":    "image/png",
			"width":           w,
			"height":          h,
			"original_bytes":  origBytes,
			"original_width":  origW,
			"original_height": origH,
			"cropped":         cropped,
			"auto_cropped":    autoCropped,
		}
		// When cropped, surface where the agent's next slice would
		// start so a multi-step read is one number away. Use
		// appliedMax (the actual crop height that took effect) so
		// slices_remaining is correct for both manual and
		// auto-crop paths.
		if cropped && origH > h && appliedMax > 0 {
			env["next_offset_y"] = h
			env["slices_remaining"] = (origH - h + appliedMax - 1) / appliedMax
		}

		// We populate THREE access paths so every client has a
		// way to actually see the image:
		//
		//   1. inline ImageContent (added below) — Claude Desktop
		//      renders natively; size-capped at inlineCap.
		//   2. content_file (local /tmp PNG) — Claude Code calls
		//      its built-in Read tool on the path; the agent's
		//      vision sees the image without going through MCP
		//      again. Only useful when MCP server and agent share
		//      a filesystem (typical local install).
		//   3. content_url (ledger daemon HTTP) — works for
		//      cross-machine / containerised setups where the
		//      agent or its WebFetch tool can reach the daemon
		//      over the network.
		//
		// Always write the local file (cheap), always try the
		// daemon upload (transparent fallback), always inline
		// when small. Costs one tiny disk write + one HTTP POST
		// per screenshot; benefit is "works in every client" with
		// no per-tool branching.
		var contentURL string
		path, werr := writeScreenshotTemp(pngBytes)
		if werr != nil {
			audit.Logf("screenshot %s write failed: %v", args.URL, werr)
			return mcp.NewToolResultError(fmt.Sprintf("write png: %v", werr)), nil
		}
		contentFile := path

		if !ledger.Disabled() {
			c := ledger.NewDaemonClient()
			if c.Reachable(ctx) {
				up, uerr := c.UploadScreenshot(ctx, pngBytes, "")
				if uerr == nil {
					contentURL = up.URL
				} else {
					audit.Logf("screenshot %s daemon upload failed: %v", args.URL, uerr)
				}
			}
		}
		audit.Logf("screenshot %s → file=%s url=%s (%d bytes, engine=%s)", args.URL, contentFile, contentURL, len(pngBytes), res.Engine)

		env["content_file"] = contentFile
		if contentURL != "" {
			env["content_url"] = contentURL
		}

		// Inline image content when small enough. Claude Desktop
		// renders this natively (no follow-up fetch); same for
		// cloud LLMs that can't reach 127.0.0.1.
		inlined := len(pngBytes) <= inlineCap
		env["inline"] = inlined
		env["read_with"] = readWithGuidance(inlined, contentURL != "", true)

		return buildScreenshotResult(env, pngBytes, inlined), nil
	}))
}

// readScreenshotArgs is the input for re-fetching a previously
// captured screenshot. Either filename (matching the daemon's
// /screenshots/<filename> route) OR a full URL the MCP server can
// fetch on the agent's behalf — agent picks based on what it has.
//
// OffsetY lets the agent paginate down a tall page: first call
// with offset_y=0 max_height=4096, see in the response that
// next_offset_y=4096 and slices_remaining=N, then call again with
// offset_y=4096 to walk the page from top to bottom.
type readScreenshotArgs struct {
	Filename  string `json:"filename,omitempty"`
	URL       string `json:"url,omitempty"`
	MaxHeight int    `json:"max_height,omitempty"`
	OffsetY   int    `json:"offset_y,omitempty"`
}

func addReadScreenshotTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_fetch_read_screenshot",
		mcp.WithDescription("Re-fetch a previously captured screenshot and return it as inline image content. Two main use cases:\n\n  1. Pagination — the original screenshot returned a slice (cropped or auto-cropped from a tall page). The companion `NOTE: this is a slice…` hint includes a `filename` and `offset_y` exactly for this call. Pass them through to read the next slice; repeat until slices_remaining=0.\n\n  2. Cross-machine — the original response carried a `content_url` the agent can't reach directly (e.g. claude.ai's cloud agent can't reach 127.0.0.1). MCP server fetches on the agent's behalf and returns the bytes inline.\n\nResponse mirrors the screenshot tool's: JSON envelope with width / height / original_height / next_offset_y / slices_remaining + an inline ImageContent block + (when this is a slice) an adjacent text hint pointing at the next offset. Defaults to cropping to 4096 px tall so the result fits the inline-image cap; pass a larger max_height when you want a single bigger slice (still capped server-side at the inline limit)."),
		mcp.WithString("filename", mcp.Description("Bare filename returned by social_fetch_screenshot (e.g. social-fetch-screenshot-12345.png). Looked up under the local screenshots dir or via the ledger daemon's GET /screenshots/<filename> route. Mutually exclusive with `url`.")),
		mcp.WithString("url", mcp.Description("Full URL of the screenshot (typically what social_fetch_screenshot returned as content_url). MCP server fetches this on the agent's behalf — useful when the agent can't reach the URL directly. Mutually exclusive with `filename`.")),
		mcp.WithNumber("max_height", mcp.Description("Crop to N pixels tall before encoding. Default 4096 (keeps the result inline-renderable). Larger values risk exceeding the per-image cap.")),
		mcp.WithNumber("offset_y", mcp.Description("Y-coordinate to start the slice at (default 0 = top of page). Use the previous response's `next_offset_y` to paginate down a tall page in 4096-px slices.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args readScreenshotArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "read_screenshot")
		defer closeAudit()

		filename := strings.TrimSpace(args.Filename)
		urlStr := strings.TrimSpace(args.URL)
		if filename == "" && urlStr == "" {
			return mcp.NewToolResultError("either filename or url is required"), nil
		}
		if filename != "" && urlStr != "" {
			return mcp.NewToolResultError("filename and url are mutually exclusive"), nil
		}

		pngBytes, source, err := loadScreenshotBytes(ctx, filename, urlStr)
		if err != nil {
			audit.Logf("read_screenshot FAILED (%s): %v", source, err)
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Original dims captured before slicing so the agent can
		// see whether more remains below the slice they're
		// reading. Critical for the multi-step read flow.
		origW, origH, _ := headless.PNGDims(pngBytes)
		origBytes := len(pngBytes)

		// Default max_height = 4096. Treat 0 as "use default" since
		// JSON-omitted ints come through as 0 and we want the
		// safe default for vision-cap reasons. Operator who really
		// wants no crop can pass a giant value (e.g. 1_000_000).
		maxH := args.MaxHeight
		if maxH <= 0 {
			maxH = readScreenshotMaxHeightDefault
		}
		offsetY := args.OffsetY
		if offsetY < 0 {
			offsetY = 0
		}

		var cropped bool
		if maxH > 0 || offsetY > 0 {
			out, didCrop, cerr := headless.CropPNGSlice(pngBytes, offsetY, maxH)
			if cerr != nil {
				audit.Logf("read_screenshot crop failed: %v", cerr)
				return mcp.NewToolResultError(cerr.Error()), nil
			}
			pngBytes = out
			cropped = didCrop
		}
		w, h, _ := headless.PNGDims(pngBytes)

		audit.Logf("read_screenshot %s (offset=%d max_h=%d → %d bytes %dx%d, cropped=%v)", source, offsetY, maxH, len(pngBytes), w, h, cropped)

		env := map[string]any{
			"source":          source,
			"content_bytes":   len(pngBytes),
			"content_type":    "image/png",
			"width":           w,
			"height":          h,
			"original_bytes":  origBytes,
			"original_width":  origW,
			"original_height": origH,
			"offset_y":        offsetY,
			"max_height":      maxH,
			"cropped":         cropped,
		}
		// Pagination hint: where the next slice starts and how
		// many slices are still left so the agent can plan a
		// multi-step read without redoing the math.
		if cropped {
			nextY := offsetY + h
			if nextY < origH {
				env["next_offset_y"] = nextY
				env["slices_remaining"] = (origH - nextY + maxH - 1) / maxH
			} else {
				env["slices_remaining"] = 0
			}
		}
		// Always inline here — that's the whole point of this tool.
		// If the resulting bytes are still over the per-image cap
		// even after crop, the LLM will reject the response and the
		// agent can retry with a smaller max_height.
		return buildScreenshotResult(env, pngBytes, true), nil
	}))
}

// loadScreenshotBytes fetches the PNG by filename (local fs +
// ledger-daemon HTTP fallback) or by URL (direct HTTP GET).
// Returns the bytes, a short source label for the audit log, and
// any error.
func loadScreenshotBytes(ctx context.Context, filename, urlStr string) ([]byte, string, error) {
	if filename != "" {
		// Try local screenshots dir first — fast, no network.
		base := filepath.Base(filename)
		if dir, err := ledger.ScreenshotsDir(); err == nil {
			full := filepath.Join(dir, base)
			if data, err := os.ReadFile(full); err == nil {
				return data, "local:" + base, nil
			}
		}
		// Fall back to the ledger daemon's GET /screenshots/<file>.
		if !ledger.Disabled() {
			c := ledger.NewDaemonClient()
			if c.Reachable(ctx) {
				return fetchHTTP(ctx, c.ScreenshotURL(base))
			}
		}
		return nil, "filename:" + base, fmt.Errorf("not found locally and ledger daemon unreachable")
	}
	return fetchHTTP(ctx, urlStr)
}

// fetchHTTP does a plain GET and returns the body bytes, capped
// at 16 MB so a misrouted response (e.g. an HTML error page) can't
// blow up the MCP server's memory.
func fetchHTTP(ctx context.Context, urlStr string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, "url:" + urlStr, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "url:" + urlStr, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "url:" + urlStr, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	const cap = 16 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, cap+1))
	if err != nil {
		return nil, "url:" + urlStr, err
	}
	if len(body) > cap {
		return nil, "url:" + urlStr, fmt.Errorf("response too large (>16 MB)")
	}
	return body, "url:" + urlStr, nil
}

// buildScreenshotResult assembles a CallToolResult that carries
// the JSON envelope as the primary content (so cloud LLMs see the
// metadata) and optionally the PNG inline as ImageContent (so
// Claude Desktop / vision-capable LLMs render it natively
// without a follow-up fetch).
//
// When the image is a slice (cropped or auto-cropped from a tall
// page), we insert a third TextContent block right before the
// ImageContent that explicitly tells the agent "this is part of
// a longer page" + how to read more. Buried-in-JSON fields like
// `next_offset_y` are easy to miss; an adjacent text hint is
// hard to skip past when the agent looks at the image.
//
// Keeping the JSON envelope ALONGSIDE the image is deliberate —
// the agent might read the metadata first (cropped status, file
// path, URL) and decide whether to even look at the image,
// especially when the response is being processed by tooling
// rather than displayed to a human.
func buildScreenshotResult(env map[string]any, png []byte, inline bool) *mcp.CallToolResult {
	body, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("marshal envelope: " + err.Error())
	}
	contents := []mcp.Content{
		mcp.TextContent{Type: mcp.ContentTypeText, Text: string(body)},
	}
	if hint := sliceHintFromEnv(env); hint != "" {
		contents = append(contents, mcp.TextContent{
			Type: mcp.ContentTypeText,
			Text: hint,
		})
	}
	if inline && len(png) > 0 && len(png) <= inlineCap {
		contents = append(contents, mcp.NewImageContent(
			base64.StdEncoding.EncodeToString(png),
			"image/png",
		))
	}
	return &mcp.CallToolResult{Content: contents}
}

// sliceHintFromEnv builds the explicit "this is a slice" message
// when the envelope describes a cropped capture. Returns empty
// string for full-page (un-cropped) results so a complete
// screenshot doesn't get a noisy "you got everything" hint.
//
// Pulls fields out of the same map the JSON envelope was built
// from, keeping one source of truth for slice arithmetic.
func sliceHintFromEnv(env map[string]any) string {
	cropped, _ := env["cropped"].(bool)
	if !cropped {
		return ""
	}
	height, _ := env["height"].(int)
	origH, _ := env["original_height"].(int)
	nextY, _ := env["next_offset_y"].(int)
	slicesLeft, _ := env["slices_remaining"].(int)
	autoCropped, _ := env["auto_cropped"].(bool)

	// content_url filename (basename) is what read_screenshot
	// wants. Fall back to content_file's basename when only that
	// path is populated.
	filename := ""
	if u, ok := env["content_url"].(string); ok && u != "" {
		// last path segment of the URL
		if idx := strings.LastIndex(u, "/"); idx >= 0 && idx+1 < len(u) {
			filename = u[idx+1:]
		}
	}
	if filename == "" {
		if f, ok := env["content_file"].(string); ok && f != "" {
			if idx := strings.LastIndex(f, "/"); idx >= 0 && idx+1 < len(f) {
				filename = f[idx+1:]
			} else {
				filename = f
			}
		}
	}

	var b strings.Builder
	if autoCropped {
		fmt.Fprintf(&b, "NOTE: this is the top %d px of a %d px tall page (auto-cropped to fit the inline-image cap).", height, origH)
	} else {
		fmt.Fprintf(&b, "NOTE: this is a %d px slice of a %d px tall page.", height, origH)
	}
	if nextY > 0 && slicesLeft > 0 {
		fmt.Fprintf(&b, " %d slice(s) remain below.", slicesLeft)
		if filename != "" {
			fmt.Fprintf(&b, " Call `social_fetch_read_screenshot` with filename=%q offset_y=%d to read the next slice.", filename, nextY)
		} else {
			fmt.Fprintf(&b, " Call `social_fetch_read_screenshot` with offset_y=%d to read the next slice.", nextY)
		}
	}
	return b.String()
}

// PNG dimension reading + cropping helpers live in
// internal/render/headless/png.go (PNGDims, CropPNGTop,
// CropPNGSlice). Both this MCP layer and the CLI screenshot
// command import the headless package, so the helpers serve
// both transports without duplication.

// readWithGuidance produces the agent-facing instruction line in
// the screenshot envelope. We branch so the agent doesn't waste
// reasoning on a path that doesn't apply (e.g. content_file when
// the MCP server and agent live on different machines).
func readWithGuidance(inlined, haveURL, haveFile bool) string {
	// Three viewing paths, listed in order from "best UX" → "still
	// works in degraded environments". Always include all that are
	// populated so the agent picks based on its capabilities.
	switch {
	case inlined && haveURL:
		return "Best path: PNG is attached inline as image content (Claude Desktop renders natively). For Claude Code, call the built-in Read tool on `content_file` to see the image rendered in chat. `content_url` works for cross-machine / remote MCP setups."
	case inlined:
		return "PNG attached inline as image content — Claude Desktop renders natively. Claude Code: call the built-in Read tool on `content_file`."
	case haveURL && haveFile:
		return "Image too large to inline. Claude Code: Read tool on `content_file` (renders in chat). Claude Desktop: social_fetch_read_file with `content_file`. Cross-machine: call social_fetch_read_screenshot with `filename` (basename of content_url) for a vision-friendly cropped slice, or fetch content_url directly when reachable."
	case haveURL:
		return "Image too large to inline. Call social_fetch_read_screenshot with `filename` (basename of content_url) for a cropped slice, or fetch content_url directly when reachable."
	default:
		return "Image too large to inline. Claude Code: Read on `content_file`. Claude Desktop: social_fetch_read_file with `content_file`. Or call social_fetch_read_screenshot for a cropped slice."
	}
}

// writeScreenshotTemp writes PNG bytes to a temp file. Mirrors
// writeContentTemp but takes []byte (not string) so the binary
// payload doesn't go through any string-conversion that could
// mangle bytes. Prefix matches social-fetch-* so safeTempPath in
// social_fetch_read_file accepts it.
func writeScreenshotTemp(png []byte) (string, error) {
	f, err := os.CreateTemp("", "social-fetch-screenshot-*.png")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(png); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
