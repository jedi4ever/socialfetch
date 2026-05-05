package mcp

// MCP tool for the screenshot capability — `social_fetch_screenshot`.
// Captures a PNG of any URL via the headless browser stack and writes
// it to a temp file under os.TempDir() with the social-fetch-prefix
// convention (so social_fetch_read_file can serve it back to MCP-only
// clients without a separate filesystem path).
//
// Why a file path (not inline base64): PNGs are binary; encoding them
// inline through MCP's JSON-RPC envelope adds ~33% bandwidth + makes
// the agent's context absorb the whole image whenever the LLM looks at
// the response. The file-output pattern (same shape used by fetch /
// research / ledger_get) keeps the LLM's view tiny — just a path +
// metadata — and lets the agent decide whether to actually open the
// image.
//
// Reading the file:
//   - Claude Code: built-in Read tool on `content_file` (markdown
//     UI auto-renders PNGs).
//   - Claude Desktop: social_fetch_read_file with the same path
//     (returns base64 chunks; the agent can stop early).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/ledger"
	"github.com/jedi4ever/social-skills/internal/render/headless"
)

type screenshotArgs struct {
	URL      string `json:"url"`
	FullPage *bool  `json:"full_page,omitempty"`
	Settle   string `json:"settle,omitempty"`
}

func addScreenshotTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_fetch_screenshot",
		mcp.WithDescription("Capture a PNG screenshot of any URL via the headless browser. Default: full-page (the entire scrollable document, not just the viewport) — matches what patai's url downloader produces. Writes the PNG to a temp file under the system tmp dir and returns a small JSON envelope with `content_file` (the path), size, dimensions implied by the engine, plus `final_url` after redirects. The agent reads the file via the Read tool (Claude Code) or social_fetch_read_file (Claude Desktop). Useful for: visual verification of what a fetched page actually looks like, capturing dynamic content for review, screenshotting before/after a UI change. Goes through the same headless daemon as social_fetch_fetch — typical 1-2s when the daemon is warm, ~3-4s when spawning a fresh Chromium. Default viewport is 1920x1080."),
		mcp.WithString("url", mcp.Required(), mcp.Description("URL to screenshot. Same redirect-handling as social_fetch_fetch.")),
		mcp.WithBoolean("full_page", mcp.Description("Capture the whole scrollable page (default true). Set false for viewport-only.")),
		mcp.WithString("settle", mcp.Description("JS-hydration wait after navigate, Go duration syntax (e.g. \"5s\", \"2500ms\"). Default 2s. Bump this for SPAs that take longer to render after their initial paint.")),
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

		env := map[string]any{
			"url":           args.URL,
			"final_url":     res.FinalURL,
			"engine":        res.Engine,
			"full_page":     fullPage,
			"content_bytes": len(res.PNG),
			"content_type":  "image/png",
		}

		// Daemon-mode preferred path: upload to the ledger daemon
		// so the resulting URL works across machines / containers.
		// MCP server, headless daemon, ledger daemon may all live
		// in different processes — only HTTP transport is
		// guaranteed; filesystem sharing is not.
		var content_file string
		var content_url string
		if !ledger.Disabled() {
			c := ledger.NewDaemonClient()
			if c.Reachable(ctx) {
				up, uerr := c.UploadScreenshot(ctx, res.PNG, "")
				if uerr == nil {
					content_url = up.URL
					audit.Logf("screenshot %s → %s (%d bytes, engine=%s, via daemon)", args.URL, up.URL, len(res.PNG), res.Engine)
				} else {
					audit.Logf("screenshot %s daemon upload failed (%v), falling back to local file", args.URL, uerr)
				}
			}
		}

		// Local fallback: write to a temp file. Used when the
		// daemon is down OR when the upload failed. Agent reads
		// via Read (Claude Code) or social_fetch_read_file
		// (Claude Desktop). Only useful when the MCP server and
		// the agent share a filesystem (local Claude Desktop
		// install) — for cross-machine MCP the content_url path
		// above is the one that works.
		if content_url == "" {
			path, werr := writeScreenshotTemp(res.PNG)
			if werr != nil {
				audit.Logf("screenshot %s write failed: %v", args.URL, werr)
				return mcp.NewToolResultError(fmt.Sprintf("write png: %v", werr)), nil
			}
			content_file = path
			audit.Logf("screenshot %s → %s (%d bytes, engine=%s, local)", args.URL, path, len(res.PNG), res.Engine)
		}

		if content_file != "" {
			env["content_file"] = content_file
		}
		if content_url != "" {
			env["content_url"] = content_url
		}
		env["read_with"] = readWithGuidance(content_url != "", content_file != "")

		return jsonResult(env)
	}))
}

// readWithGuidance produces the agent-facing instruction line in
// the screenshot envelope. We branch so the agent doesn't waste
// reasoning on a path that doesn't apply (e.g. content_file when
// the MCP server and agent live on different machines).
func readWithGuidance(haveURL, haveFile bool) string {
	switch {
	case haveURL && haveFile:
		return "Prefer `content_url` (works cross-machine via the ledger daemon). Fall back to `content_file` when the URL is unreachable: Claude Code uses the built-in Read tool, Claude Desktop uses social_fetch_read_file."
	case haveURL:
		return "Fetch `content_url` — the ledger daemon serves the PNG over HTTP. Works for cross-machine and remote MCP setups."
	default:
		return "Read `content_file`. Claude Code: built-in Read tool (auto-renders PNG). Claude Desktop: social_fetch_read_file with this `content_file` as `path`."
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
