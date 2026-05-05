package main

// CLI for `social-fetch screenshot` — capture a PNG of any URL via
// the headless browser stack. Goes through the daemon's warm pool
// when one is reachable (fast: ~1-2s) and falls back to spawning a
// fresh in-process Chromium otherwise (~3-4s warm-up).
//
// Default output path: a stable temp file pattern (one per URL slug
// + timestamp), so agents can call this without specifying -o and
// pass the returned path straight to a Read tool. Pass `-o -` to
// stream the PNG to stdout for piping into ImageMagick / file etc.

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/render/headless"
)

func runScreenshot(args []string) error {
	fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
	out := fs.String("o", "", "output path for the PNG (default: temp file). Use `-` for stdout.")
	fullPage := fs.Bool("full-page", true, "capture the entire scrollable page (default true; set false for viewport-only)")
	settle := fs.Duration("settle", 0, "JS-hydration wait after navigate (default: daemon/in-process default of 2s)")
	viewport := fs.String("viewport", "", "viewport WxH override, e.g. `1280x720`. Only honoured on the in-process path; daemon mode uses the slot's launched viewport.")
	timeout := fs.Duration("timeout", 0, "per-call timeout including browser launch (default 60s)")
	maxHeight := fs.Int("max-height", 0, "crop the captured PNG to the top N pixels (0 = no crop). Useful when feeding the result into a vision tool with a per-image size cap.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printScreenshotHelp()
		return fmt.Errorf("screenshot: <url> required")
	}
	target := fs.Arg(0)
	if _, err := url.Parse(target); err != nil {
		return fmt.Errorf("screenshot: invalid URL %q: %w", target, err)
	}

	opts := headless.OptionsFromEnv()
	if *timeout > 0 {
		opts.Timeout = *timeout
	}
	if *viewport != "" {
		w, h, err := parseViewport(*viewport)
		if err != nil {
			return fmt.Errorf("screenshot: --viewport: %w", err)
		}
		opts.ViewportWidth = w
		opts.ViewportHeight = h
	}
	f := headless.NewWithOptions(opts)

	ctx := context.Background()
	if *timeout > 0 {
		// Parent-level cap so a wedged daemon HTTP roundtrip can't
		// pin the CLI forever. The headless package's own per-call
		// Timeout bounds browser work separately.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	res, err := f.Screenshot(ctx, target, headless.ScreenshotOptions{
		FullPage: *fullPage,
		Settle:   *settle,
	})
	if err != nil {
		return fmt.Errorf("screenshot: %w", err)
	}

	// Optional post-capture crop. Done in CLI / MCP layer (not the
	// headless driver) so the cap is consistent regardless of the
	// engine that produced the bytes. Vision tools have per-image
	// size limits; cropping a 30 000 px tall page to 4096 keeps
	// the PNG under those caps.
	if *maxHeight > 0 {
		out, _, cerr := headless.CropPNGTop(res.PNG, *maxHeight)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "screenshot: crop failed: %v (using uncropped image)\n", cerr)
		} else {
			res.PNG = out
		}
	}

	// stdout passthrough — useful for piping into ImageMagick or
	// `file -` to inspect headers.
	if *out == "-" {
		_, werr := os.Stdout.Write(res.PNG)
		return werr
	}

	path := *out
	if path == "" {
		path = defaultScreenshotPath(target)
	}
	if err := os.WriteFile(path, res.PNG, 0o644); err != nil {
		return fmt.Errorf("screenshot: write %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "screenshot: %d bytes → %s (engine=%s, final=%s)\n",
		len(res.PNG), path, res.Engine, res.FinalURL)
	// stdout = the path so the screenshot fits into a shell pipeline
	// (`OPEN=$(social-fetch screenshot $URL); open "$OPEN"`).
	fmt.Println(path)
	return nil
}

// parseViewport accepts the WxH form and returns dimensions.
// Allowed range: 320..3840 wide, 240..2160 tall — sanity bounds
// that prevent degenerate values from blowing up Chrome.
func parseViewport(s string) (int, int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected WxH, got %q", s)
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("width: %w", err)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("height: %w", err)
	}
	if w < 320 || w > 3840 || h < 240 || h > 2160 {
		return 0, 0, fmt.Errorf("dimensions out of range (320x240 .. 3840x2160), got %dx%d", w, h)
	}
	return w, h, nil
}

// screenshotSlugRE matches anything that's NOT alphanumeric or
// dashes — used to derive a filesystem-safe slug from URLs for the
// default temp-file name.
var screenshotSlugRE = regexp.MustCompile(`[^a-zA-Z0-9-]+`)

// defaultScreenshotPath builds a stable temp-file path for the given
// URL: `<TempDir>/social-fetch-screenshot-<slug>-<unix>.png`. Slug is
// host + first path segment, normalised to alphanum + dashes. The
// `social-fetch-` prefix matches the convention used by writeContentTemp
// so social_fetch_read_file can read these files in MCP-only clients.
func defaultScreenshotPath(rawURL string) string {
	slug := "page"
	if u, err := url.Parse(rawURL); err == nil {
		host := strings.TrimPrefix(u.Host, "www.")
		first := ""
		if u.Path != "" && u.Path != "/" {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) > 0 {
				first = parts[0]
			}
		}
		s := host
		if first != "" {
			s += "-" + first
		}
		s = screenshotSlugRE.ReplaceAllString(s, "-")
		s = strings.Trim(s, "-")
		if len(s) > 64 {
			s = s[:64]
		}
		if s != "" {
			slug = s
		}
	}
	name := fmt.Sprintf("social-fetch-screenshot-%s-%d.png", slug, time.Now().Unix())
	return os.TempDir() + string(os.PathSeparator) + name
}

func printScreenshotHelp() {
	fmt.Print(`social-fetch screenshot — capture a full-page PNG of any URL

Usage:
  social-fetch screenshot <url> [flags]

Flags:
  -o <path>          output path (default: $TMPDIR/social-fetch-screenshot-<slug>-<unix>.png).
                     Use ` + "`-`" + ` to stream PNG bytes to stdout.
  --full-page        capture the whole scrollable page (default true)
  --settle DUR       JS-hydration wait after navigate (default: 2s)
  --viewport WxH     viewport override, e.g. 1280x720 (in-process path only;
                     daemon mode uses the slot's launched viewport)
  --timeout DUR      per-call timeout including browser launch (default 60s)

Examples:
  social-fetch screenshot https://news.ycombinator.com
  social-fetch screenshot https://example.com -o /tmp/example.png
  social-fetch screenshot https://example.com --viewport 1280x720 --full-page=false
  social-fetch screenshot https://example.com -o - | open -f -a Preview

Daemon: when the headless daemon is up (` + "`social-fetch headless start`" + `)
the call goes through the warm browser pool — typical 1-2s. Without it,
each call spawns a fresh Chromium (~3-4s). Either path is auto-detected.
`)
}
