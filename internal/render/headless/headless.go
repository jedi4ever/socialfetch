// Package headless drives a local headless browser to fetch JS-rendered
// HTML for sites that resist plain HTTP. Today's implementation uses
// chromedp (talks CDP directly to the user's installed Chrome /
// Chromium); the public surface is intentionally engine-neutral so a
// future second engine (Playwright via playwright-go) can drop in
// behind the same Fetcher type.
//
// Why this exists alongside `bridge` and `htmlmd.JinaReader`:
//
//   - bridge: drives the user's logged-in Chrome via the social-fetch
//     extension. Highest fidelity (auth cookies, real session) but
//     requires the bridge daemon + extension running.
//   - jina:   service-backed (r.jina.ai) — anonymous, JS-rendered,
//     remote. Free tier rate-limited; sends the URL to a third party.
//   - headless (this): anonymous AND local. Spawns a fresh Chromium
//     per fetch with stealth defaults, optionally injects auth
//     cookies (LinkedIn LI_AT). Slower than http but doesn't need
//     the bridge OR the network round-trip to Jina.
//
// Stealth defaults are ported from patai/providers/browser_common.py
// (the user's Python downloader) — the same UA / locale / init
// script / args proven to work against LinkedIn's bot detection.
package headless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Options shapes the headless fetch. Defaults match the Python
// downloader's stealth profile; operators override per-call via env
// vars (see OptionsFromEnv) or via a constructed Options struct.
type Options struct {
	// Headless toggles the visible-window mode. true = no window
	// (production default); false = a real Chrome window pops up
	// (useful for debugging — watch the page render).
	Headless bool

	// Timeout is the per-fetch deadline including browser launch
	// + navigation + content read. Chrome cold-start adds
	// ~500ms-1s; budget at least 30s for slow pages on top.
	Timeout time.Duration

	// UserAgent is the navigator.userAgent string. Empty means
	// "use chromedp's default" (which advertises HeadlessChrome —
	// trivially detectable). The default below is a real-Chrome UA
	// that paired with the stealth init script makes us look like a
	// regular browser.
	UserAgent string

	// Locale + Timezone shape Intl.Locale + Intl.DateTimeFormat —
	// some bot-detection scripts cross-check these against the IP
	// region. We default to en-US / America/New_York to match
	// what the Python code uses; override for non-US-region tests.
	Locale   string
	Timezone string

	// ViewportWidth + ViewportHeight set window.innerWidth /
	// innerHeight. Real-browser-like defaults (1920x1080) help the
	// stealth profile.
	ViewportWidth  int
	ViewportHeight int

	// Cookies is an optional list of cookies to inject before
	// navigation. Used for sites that need auth (e.g. LinkedIn's
	// `li_at` session cookie). Empty = anonymous fetch.
	Cookies []Cookie

	// Settle is the time we sleep after `body` becomes ready,
	// before reading outerHTML. Pages that hydrate via JS (Medium,
	// most React/Next.js apps) finish DOMContentLoaded with an empty
	// article container — the actual prose appears 1-3s later.
	// Without a settle delay we'd see a near-empty body.
	//
	// Default 2s matches the Python downloader's random_delay(2, 4)
	// floor; bump higher for slow-hydrating sites via env var.
	Settle time.Duration

	// ExecPath overrides the Chrome / Chromium binary location.
	// Empty = chromedp auto-detects on PATH (works on macOS via
	// Chrome.app, on Linux via /usr/bin/google-chrome or chromium).
	ExecPath string
}

// Cookie is a single name/value/domain/path triple injected into
// the browser before navigation. The five fields cover what
// LinkedIn's li_at and similar session cookies need.
type Cookie struct {
	Name     string
	Value    string
	Domain   string // e.g. ".linkedin.com"
	Path     string // e.g. "/"
	HTTPOnly bool
	Secure   bool
}

// DefaultOptions is the single source of truth for headless fetch
// behaviour. Mirrors patai/providers/browser_common.py:
//
//   - Real-Chrome UA (no "HeadlessChrome" giveaway)
//   - 1920x1080 viewport
//   - en-US / America/New_York
//   - 60s timeout (matches the Jina default)
var DefaultOptions = Options{
	Headless:       true,
	Timeout:        60 * time.Second,
	UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	Locale:         "en-US",
	Timezone:       "America/New_York",
	ViewportWidth:  1920,
	ViewportHeight: 1080,
	Settle:         2 * time.Second,
}

// stealthScript runs at every navigation before any page script —
// masks the standard automation tells. Direct port from the Python
// codebase's _STEALTH_INIT_SCRIPT.
const stealthScript = `
Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
Object.defineProperty(navigator, 'languages', { get: () => ['en-US', 'en'] });
Object.defineProperty(navigator, 'plugins', { get: () => [1, 2, 3, 4, 5] });
window.chrome = { runtime: {} };
`

// Result is what a successful fetch returns. HTML is the rendered
// outerHTML after the page is settled; FinalURL is the post-redirect
// URL the browser actually landed on (which can differ from the
// requested URL if the site redirects). Engine names which underlying
// driver served the request — useful in audit logs when we add
// playwright-go as an alternative.
type Result struct {
	HTML     string
	FinalURL string
	Engine   string
}

// Fetcher is the headless transport. Cheap to construct — no
// resources held until Fetch is called. Each Fetch spawns a fresh
// browser process; for high-frequency batch use a future pool can
// reuse contexts, but for chain-fallback (one fetch per URL, used
// only when http/bridge fail) per-call lifecycle keeps the code
// simple and avoids long-lived browser leaks.
type Fetcher struct {
	Options Options
}

// New builds a Fetcher with DefaultOptions overlaid by env vars.
// Tests / specific call sites that need explicit control should use
// NewWithOptions(opts).
func New() *Fetcher {
	return NewWithOptions(OptionsFromEnv())
}

// NewWithOptions builds a Fetcher from explicit options. Empty
// fields fall back to DefaultOptions equivalents so callers can set
// just the field they care about.
func NewWithOptions(opts Options) *Fetcher {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultOptions.Timeout
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultOptions.UserAgent
	}
	if opts.Locale == "" {
		opts.Locale = DefaultOptions.Locale
	}
	if opts.Timezone == "" {
		opts.Timezone = DefaultOptions.Timezone
	}
	if opts.ViewportWidth == 0 {
		opts.ViewportWidth = DefaultOptions.ViewportWidth
	}
	if opts.ViewportHeight == 0 {
		opts.ViewportHeight = DefaultOptions.ViewportHeight
	}
	if opts.Settle == 0 {
		opts.Settle = DefaultOptions.Settle
	}
	return &Fetcher{Options: opts}
}

// OptionsFromEnv reads SOCIAL_FETCH_HEADLESS_* env vars and overlays
// them on DefaultOptions. Bad values fall through to defaults rather
// than failing — same fail-soft policy as the Jina knobs.
//
//	SOCIAL_FETCH_HEADLESS_HEADLESS    true (default) | false
//	SOCIAL_FETCH_HEADLESS_TIMEOUT     60s (default), any time.ParseDuration
//	SOCIAL_FETCH_HEADLESS_SETTLE      2s (default) — post-navigate hydration delay
//	SOCIAL_FETCH_HEADLESS_USER_AGENT  custom UA string
//	SOCIAL_FETCH_HEADLESS_EXEC_PATH   path to chrome/chromium binary
//
// Note: no auth-cookie env var. The headless transport is intended
// for anonymous fetches — LinkedIn's guest-preview, Medium's free
// excerpt, etc. all render fine without authentication. Callers
// that want to inject a session cookie programmatically (e.g. for
// a future auth-aware headless flow) can build an Options{Cookies:
// ...} directly via NewWithOptions.
func OptionsFromEnv() Options {
	opts := DefaultOptions
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_HEADLESS_HEADLESS")); v != "" {
		switch strings.ToLower(v) {
		case "false", "0", "no", "off":
			opts.Headless = false
		case "true", "1", "yes", "on":
			opts.Headless = true
		}
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_HEADLESS_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			opts.Timeout = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_HEADLESS_SETTLE")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			opts.Settle = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_HEADLESS_USER_AGENT")); v != "" {
		opts.UserAgent = v
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_HEADLESS_EXEC_PATH")); v != "" {
		opts.ExecPath = v
	}
	return opts
}

// Fetch loads the URL via the headless daemon when one is reachable,
// otherwise falls back to spawning a fresh stealth Chromium in-process.
// The daemon path is much faster (no per-call ~2s Chrome warmup),
// the in-process path keeps single-shot CLI usage working with no
// daemon dependency.
//
// Daemon discovery is via SOCIAL_FETCH_HEADLESS_DAEMON_URL (default
// http://127.0.0.1:5556 — what `social-fetch headless start` listens
// on). Probe is capped at 250 ms so the overhead on every fetch is
// bounded regardless of daemon state.
//
// In-process path: every call gets a brand-new browser context — no
// session reuse — so a previous fetch's cookies / localStorage /
// cache can't bleed into the next. Cookies in opts.Cookies are
// injected before navigation; cookies scoped to a domain that
// doesn't match the requested URL stay inert (Chrome's same-origin
// policy filters them server-side).
//
// Cookies are NOT honoured in daemon mode today — the daemon is
// anonymous-only. Callers that need cookie injection must use
// NewWithOptions(Options{Cookies: ...}) which always takes the
// in-process path (the daemon probe is skipped when cookies are set).
func (f *Fetcher) Fetch(ctx context.Context, raw string) (*Result, error) {
	if raw == "" {
		return nil, errors.New("headless: empty URL")
	}

	// Daemon-mode fast path: skip when the call needs cookies (the
	// daemon doesn't accept them) or when the operator explicitly
	// disabled it via env. Otherwise probe for the daemon and use
	// it transparently.
	if len(f.Options.Cookies) == 0 && os.Getenv("SOCIAL_FETCH_HEADLESS_DAEMON_DISABLE") == "" {
		client := NewDaemonClient()
		if client.Reachable(ctx) {
			// Forward the caller's explicit Settle to the daemon
			// when it differs from the default — that's how the
			// article fetcher's "retry with longer settle" path
			// gets the daemon to wait extra on a SPA shell.
			settle := time.Duration(0)
			if f.Options.Settle != DefaultOptions.Settle {
				settle = f.Options.Settle
			}
			return client.FetchWithSettle(ctx, raw, settle)
		}
	}

	allocOpts := buildAllocatorOpts(f.Options)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	// Bound the whole fetch (browser launch + navigate + content)
	// by the Timeout. Without this, a hung navigation can pin the
	// chromedp goroutine indefinitely.
	timedCtx, cancelTimeout := context.WithTimeout(browserCtx, f.Options.Timeout)
	defer cancelTimeout()

	var (
		html     string
		finalURL string
	)

	actions := []chromedp.Action{
		// Network must be enabled before SetCookies works.
		network.Enable(),
		// Stealth init script runs in every frame before any page
		// script, masking the automation tells.
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(stealthScript).Do(ctx)
			return err
		}),
	}
	if cookies := relevantCookies(raw, f.Options.Cookies); len(cookies) > 0 {
		actions = append(actions, setCookiesAction(cookies))
	}
	actions = append(actions,
		chromedp.Navigate(raw),
		chromedp.WaitReady("body", chromedp.ByQuery),
	)
	if f.Options.Settle > 0 {
		actions = append(actions, chromedp.Sleep(f.Options.Settle))
	}
	actions = append(actions,
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
		chromedp.Location(&finalURL),
	)

	if err := chromedp.Run(timedCtx, actions...); err != nil {
		return nil, fmt.Errorf("headless: %w", err)
	}
	if finalURL == "" {
		finalURL = raw
	}
	return &Result{HTML: html, FinalURL: finalURL, Engine: "chromedp"}, nil
}

// buildAllocatorOpts assembles the chromedp.ExecAllocator options
// from a Headless Options struct. Args mirror the Python code's
// browser launch: disable-blink-features=AutomationControlled +
// no-sandbox + disable-dev-shm-usage so we run cleanly in containers
// and don't trip the "Chrome is being controlled by automated test
// software" banner detection.
func buildAllocatorOpts(opts Options) []chromedp.ExecAllocatorOption {
	a := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.UserAgent(opts.UserAgent),
		chromedp.WindowSize(opts.ViewportWidth, opts.ViewportHeight),
	}
	if opts.Headless {
		a = append(a, chromedp.Headless)
	}
	if opts.ExecPath != "" {
		a = append(a, chromedp.ExecPath(opts.ExecPath))
	}
	if opts.Locale != "" {
		a = append(a, chromedp.Flag("lang", opts.Locale))
	}
	if opts.Timezone != "" {
		// chromedp doesn't have a top-level timezone option; pass
		// it via TZ env. The exec allocator inherits process env so
		// we set it on the running process for the spawn.
		// (Per-spawn TZ would be cleaner; deferred until needed.)
		_ = os.Setenv("TZ", opts.Timezone)
	}
	return a
}

// relevantCookies filters opts.Cookies down to just those whose
// Domain matches the requested URL's host. Why bother filtering
// rather than letting Chrome do it: chromedp's SetCookies fails
// loudly when given a cookie whose domain is unrelated to any
// page yet visited (the cookie store is empty + URL-attached).
func relevantCookies(rawURL string, cookies []Cookie) []Cookie {
	host := hostOf(rawURL)
	if host == "" {
		return nil
	}
	var out []Cookie
	for _, c := range cookies {
		if c.Domain == "" {
			continue
		}
		// Match the cookie domain rule: cookie domain ".foo.com"
		// matches host "foo.com" OR "*.foo.com".
		d := strings.TrimPrefix(c.Domain, ".")
		if host == d || strings.HasSuffix(host, "."+d) {
			out = append(out, c)
		}
	}
	return out
}

// setCookiesAction wraps cdproto's network.SetCookies in a
// chromedp.Action so it composes with chromedp.Run. We set
// cookies *after* network.Enable() so the network domain is
// initialised; the actual call doesn't navigate.
func setCookiesAction(cookies []Cookie) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		params := make([]*network.CookieParam, 0, len(cookies))
		// Cookies are injected with Expires 24h out so a short-lived
		// session cookie doesn't get auto-evicted before navigation
		// lands.
		expires := cdp.TimeSinceEpoch(time.Now().Add(24 * time.Hour))
		for _, c := range cookies {
			path := c.Path
			if path == "" {
				path = "/"
			}
			params = append(params, &network.CookieParam{
				Name:     c.Name,
				Value:    c.Value,
				Domain:   c.Domain,
				Path:     path,
				HTTPOnly: c.HTTPOnly,
				Secure:   c.Secure,
				Expires:  &expires,
			})
		}
		return network.SetCookies(params).Do(ctx)
	}
}

// hostOf parses a URL and returns its lower-case host with any
// "www." prefix stripped. Cheap host-only normaliser used by
// relevantCookies — we don't need full URL parsing semantics here.
func hostOf(raw string) string {
	// Skip scheme.
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Trim path / query / fragment.
	for _, sep := range []string{"/", "?", "#"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	// Trim port.
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(strings.ToLower(s), "www.")
}
