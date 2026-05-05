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
// ~33 % overhead; we leave 2x headroom on top for the rest of
// the response context.
const inlineCap = 1_500_000

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
		mcp.WithDescription("Capture a PNG screenshot of any URL via the headless browser. Default: full-page (the whole scrollable document, not just the viewport) — matches what patai's url downloader produces. Returns BOTH a JSON envelope (with content_file path + content_url for the ledger daemon) AND the PNG inline as image content when the page is small enough to fit Anthropic's per-image cap (~1.5 MB raw). Larger pages return only the envelope; the agent then calls social_fetch_read_screenshot with a max_height to get a viewable slice. Useful for: visual verification of a fetched page, capturing dynamic content, before/after UI checks. Goes through the same headless daemon pool as social_fetch_fetch — typical 1-2s when the daemon is warm. Default viewport 1920x1080."),
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
		var cropped bool
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
		}
		// When cropped, surface where the agent's next slice would
		// start so a multi-step read is one number away.
		if cropped && origH > h {
			env["next_offset_y"] = h
			env["slices_remaining"] = (origH - h + args.MaxHeight - 1) / args.MaxHeight
		}

		// Daemon-mode preferred path: upload to the ledger daemon
		// so the resulting URL works across machines / containers.
		// MCP server, headless daemon, ledger daemon may all live
		// in different processes — only HTTP transport is
		// guaranteed; filesystem sharing is not.
		var contentFile string
		var contentURL string
		if !ledger.Disabled() {
			c := ledger.NewDaemonClient()
			if c.Reachable(ctx) {
				up, uerr := c.UploadScreenshot(ctx, pngBytes, "")
				if uerr == nil {
					contentURL = up.URL
					audit.Logf("screenshot %s → %s (%d bytes, engine=%s, via daemon)", args.URL, up.URL, len(pngBytes), res.Engine)
				} else {
					audit.Logf("screenshot %s daemon upload failed (%v), falling back to local file", args.URL, uerr)
				}
			}
		}

		// Local fallback: write to a temp file so co-located
		// agents (Claude Code on the same machine) can read it
		// via filesystem. Only useful when MCP server and agent
		// share a filesystem; cross-machine agents rely on the
		// inline ImageContent or content_url paths.
		if contentURL == "" {
			path, werr := writeScreenshotTemp(pngBytes)
			if werr != nil {
				audit.Logf("screenshot %s write failed: %v", args.URL, werr)
				return mcp.NewToolResultError(fmt.Sprintf("write png: %v", werr)), nil
			}
			contentFile = path
			audit.Logf("screenshot %s → %s (%d bytes, engine=%s, local)", args.URL, path, len(pngBytes), res.Engine)
		}

		if contentFile != "" {
			env["content_file"] = contentFile
		}
		if contentURL != "" {
			env["content_url"] = contentURL
		}

		// Inline image content when small enough. Claude Desktop
		// renders this natively (no follow-up fetch); same for
		// cloud LLMs that can't reach 127.0.0.1.
		inlined := len(pngBytes) <= inlineCap
		env["inline"] = inlined
		env["read_with"] = readWithGuidance(inlined, contentURL != "", contentFile != "")

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
		mcp.WithDescription("Re-fetch a previously captured screenshot through the MCP channel and return it as inline image content. Use this when social_fetch_screenshot returned `inline: false` (image too big to embed in the original response) OR when the agent can't reach `content_url` directly (cloud LLMs can't fetch 127.0.0.1). The MCP server reads from the ledger daemon's screenshots dir (or fetches the URL on the agent's behalf) and returns the PNG inline. Defaults to cropping the result to top 4096 pixels so the response stays under the per-image cap; pass max_height=0 to disable cropping (caller takes responsibility for size)."),
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
	if inline && len(png) > 0 && len(png) <= inlineCap {
		contents = append(contents, mcp.NewImageContent(
			base64.StdEncoding.EncodeToString(png),
			"image/png",
		))
	}
	return &mcp.CallToolResult{Content: contents}
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
	if inlined {
		return "PNG attached inline as image content — Claude Desktop / vision-capable clients render it natively. content_url / content_file are also provided for clients that prefer URL or filesystem paths."
	}
	switch {
	case haveURL && haveFile:
		return "Image too large to inline. Call social_fetch_read_screenshot with `filename` (extract the basename from content_url) for a vision-friendly cropped slice. Or fetch content_url directly when reachable; or read content_file when MCP server and agent share a filesystem."
	case haveURL:
		return "Image too large to inline. Call social_fetch_read_screenshot with `filename` (extract the basename from content_url) to get a cropped slice. Or fetch content_url directly when reachable."
	default:
		return "Image too large to inline. Read content_file (Claude Code: Read tool; Claude Desktop: social_fetch_read_file) — or call social_fetch_read_screenshot with `filename` (basename of content_file) for a cropped vision-friendly slice."
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
