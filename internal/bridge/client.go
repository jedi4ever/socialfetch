package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// navigateMu serializes Navigate→GetTabHTML pairs across all bridge
// clients in this process. Without it, two concurrent GetHTML calls
// would issue two `navigate` commands to the extension; the extension
// honors the active tab's URL last-write-wins, so both `get_html`s
// would scrape whichever page won the navigate race — silently
// returning the wrong content rather than erroring out.
//
// Bridge-bound research/fetch work effectively serializes through
// this mutex. Direct HTTP fetches (article/medium/substack fallback,
// every other platform) are unaffected.
var navigateMu sync.Mutex

// SessionLock acquires the bridge lock for a multi-step interaction
// (Navigate + several GetTabHTML/Scroll cycles, e.g. LinkedIn
// timeline / search infinite-scroll). Caller must invoke the
// returned unlock function — typically via `defer` — exactly once.
//
// Single-shot fetches should use Client.GetHTML, which acquires the
// lock internally. SessionLock is for callers that drive the bridge
// across multiple round-trips and need the active tab pinned on
// their URL the whole time.
func SessionLock() (unlock func()) {
	navigateMu.Lock()
	return navigateMu.Unlock
}

// DefaultEndpoint is the local /cmd URL fetchers POST to when the
// operator hasn't overridden it. The browser-bridge daemon runs
// on 127.0.0.1:5555 by default.
const DefaultEndpoint = "http://127.0.0.1:5555/cmd"

// EndpointEnv lets operators point fetchers at a non-default
// bridge daemon — useful when running multiple sandboxed browser
// profiles on different ports, or pointing the bridge at a remote
// machine via an SSH-forwarded port. Empty / unset → DefaultEndpoint.
const EndpointEnv = "SOCIAL_BRIDGE_URL"

// Endpoint returns the configured bridge URL, honoring
// $SOCIAL_BRIDGE_URL when set and falling back to DefaultEndpoint.
// Single source of truth for "where do fetchers POST to" so the
// .mcpb / .mcp.json env-passthrough wiring only has to declare
// one variable.
func Endpoint() string {
	if v := os.Getenv(EndpointEnv); v != "" {
		return v
	}
	return DefaultEndpoint
}

// Client is a thin wrapper over the bridge's HTTP /cmd endpoint. It's
// the connecting tissue between fetchers and the browser extension —
// each call sends a navigate then a get_html, returning the rendered
// HTML, the canonical URL the browser ended on, and the page title.
//
// The client never starts the bridge itself; if /cmd isn't reachable
// callers receive ErrBridgeUnreachable and decide whether to fall back
// to direct HTTP (Medium/Substack) or surface the error (LinkedIn).
type Client struct {
	Endpoint string
}

func NewClient() *Client { return &Client{Endpoint: DefaultEndpoint} }

// ErrBridgeUnreachable signals the bridge daemon isn't running. Used
// by callers to branch to a fallback. NOTE: this is the
// "couldn't establish a TCP connection" case — when the bridge IS
// running but the page-load takes longer than the timeout, callers
// see ErrBridgeTimeout instead so they don't misdiagnose the
// failure as "go start the daemon".
var ErrBridgeUnreachable = errors.New("bridge: daemon not running")

// ErrBridgeTimeout signals the bridge accepted the connection but
// didn't reply within the configured timeout. Distinct from
// ErrBridgeUnreachable so the agent / operator gets actionable
// guidance: "page may be heavy, bump SOCIAL_BRIDGE_TIMEOUT" rather
// than "daemon not running" (which is the wrong fix here).
var ErrBridgeTimeout = errors.New("bridge: request timed out")

// ErrNoExtension signals the bridge is up but no extension is attached.
// A separate sentinel so callers can distinguish "ran but failed" from
// "couldn't connect".
var ErrNoExtensionAttached = errors.New("bridge: no extension attached")

// reply is the shape of the JSON the extension returns for get_html.
type reply struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	HTML   string `json:"html"`
	URL    string `json:"url"`
	Title  string `json:"title"`
}

// GetHTML drives the browser to the given URL and returns the rendered
// HTML, the final URL (after any redirects), and the page title.
//
// The two-step navigate→get_html is required because the extension's
// tab matcher broadens to origin-only patterns; a single get_html may
// scrape whichever tab on the same origin is already open.
//
// Holds navigateMu for the whole pair so concurrent callers (the
// research orchestrator's parallel angles, two CLI invocations
// running at once) don't trample each other's active-tab URL. See
// navigateMu's doc for the full rationale.
func (c *Client) GetHTML(ctx context.Context, target string, audit *core.AuditLogger) (htmlStr, finalURL, title string, err error) {
	navigateMu.Lock()
	defer navigateMu.Unlock()
	if err := c.Navigate(ctx, target, audit); err != nil {
		return "", "", "", err
	}
	return c.GetTabHTML(ctx, target, audit)
}

// Navigate tells the extension to drive the browser to target. Returns
// when the page reports load complete (or the bridge command timeout
// trips, whichever comes first).
func (c *Client) Navigate(ctx context.Context, target string, audit *core.AuditLogger) error {
	audit.Logf("bridge: navigate %s", target)
	_, err := c.call(ctx, c.endpoint(), map[string]any{
		"command": "navigate",
		"url":     target,
	})
	return err
}

// Scroll asks the extension to scrollBy(0, amount) on the active tab
// and returns the resulting scrollY. Use it when a target page lazily
// loads more content as the user scrolls (LinkedIn recent-activity,
// X timelines, infinite-scroll Reddit). Pair with a sleep before the
// next get_html so the lazy-loaded items have time to render.
//
// scrollY is decoded as float64 because Chrome returns subpixel
// positions (e.g. 9776.5) for hi-DPI displays — truncating to int is
// fine since the caller only uses it for audit/logging.
func (c *Client) Scroll(ctx context.Context, amount int, audit *core.AuditLogger) (int, error) {
	if amount <= 0 {
		amount = 1500
	}
	audit.Logf("bridge: scroll +%d", amount)
	body, err := c.call(ctx, c.endpoint(), map[string]any{
		"command": "scroll",
		"amount":  amount,
	})
	if err != nil {
		return 0, err
	}
	var r struct {
		Status  string  `json:"status"`
		Error   string  `json:"error,omitempty"`
		ScrollY float64 `json:"scrollY"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, fmt.Errorf("bridge: decode scroll reply: %w", err)
	}
	if r.Status != "ok" {
		return 0, fmt.Errorf("bridge: scroll error: %s", r.Error)
	}
	return int(r.ScrollY), nil
}

// ScrollToBottom moves the largest scrollable element on the active
// tab to its bottom (scrollHeight - clientHeight). Viewport-independent
// — no amount math. Used by lazy-load loops where the goal is "hit
// the bottom and dwell" so an IntersectionObserver can fire and
// fetch more content. Returns the resulting clientHeight so the
// caller can size follow-up wheel events relative to the viewport.
func (c *Client) ScrollToBottom(ctx context.Context, audit *core.AuditLogger) (clientHeight int, err error) {
	audit.Logf("bridge: scroll_to_bottom")
	body, err := c.call(ctx, c.endpoint(), map[string]any{
		"command": "scroll_to_bottom",
	})
	if err != nil {
		return 0, err
	}
	var r struct {
		Status       string  `json:"status"`
		Error        string  `json:"error,omitempty"`
		ClientHeight float64 `json:"clientHeight"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, fmt.Errorf("bridge: decode scroll_to_bottom reply: %w", err)
	}
	if r.Status != "ok" {
		return 0, fmt.Errorf("bridge: scroll_to_bottom error: %s", r.Error)
	}
	return int(r.ClientHeight), nil
}

// Wheel dispatches a synthetic wheel event at the center of the
// viewport. Some SPAs (LinkedIn's new SDUI) lazy-load on real
// wheel events but ignore plain scrollBy() — wheel events bubble
// through React's event delegation and look more like
// user-initiated scrolling. deltaY of 0 sends 1000px (sensible
// default; callers usually pick something close to a viewport).
func (c *Client) Wheel(ctx context.Context, deltaY int, audit *core.AuditLogger) error {
	if deltaY == 0 {
		deltaY = 1000
	}
	audit.Logf("bridge: wheel deltaY=%d", deltaY)
	body, err := c.call(ctx, c.endpoint(), map[string]any{
		"command": "wheel",
		"deltaY":  deltaY,
	})
	if err != nil {
		return err
	}
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("bridge: decode wheel reply: %w", err)
	}
	if r.Status != "ok" {
		return fmt.Errorf("bridge: wheel error: %s", r.Error)
	}
	return nil
}

// GetTabHTML scrapes the active tab on the current origin without
// navigating. Use it when you've already called Navigate (or have just
// scrolled) and want the rendered DOM as it stands. The target URL is
// passed only so the extension can match the right tab — no second
// navigation is performed.
func (c *Client) GetTabHTML(ctx context.Context, target string, audit *core.AuditLogger) (htmlStr, finalURL, title string, err error) {
	audit.Logf("bridge: get_html")
	body, err := c.call(ctx, c.endpoint(), map[string]any{
		"command": "get_html",
		"url":     target,
	})
	if err != nil {
		return "", "", "", err
	}
	var r reply
	if err := json.Unmarshal(body, &r); err != nil {
		return "", "", "", fmt.Errorf("bridge: decode reply: %w", err)
	}
	if r.Status != "ok" {
		return "", "", "", fmt.Errorf("bridge: extension error: %s", r.Error)
	}
	if r.HTML == "" {
		return "", "", "", fmt.Errorf("bridge: empty HTML for %s", target)
	}
	return r.HTML, r.URL, r.Title, nil
}

func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return DefaultEndpoint
}

// bridgeHTTPClient is the dedicated HTTP client for bridge calls.
// Separate from core.HTTPClient because:
//
//  1. Timeout is much longer (default 90s, configurable via
//     SOCIAL_BRIDGE_TIMEOUT). LinkedIn / Medium / Substack page
//     hydration through a real browser regularly takes 30-60s for
//     heavy posts; the global 30s timeout cuts these mid-load and
//     produces the misleading "daemon not running" error.
//  2. No cookie jar is needed — the BROWSER (extension) carries
//     the relevant session cookies, the bridge HTTP path is
//     localhost-only RPC.
//  3. Reuses core.HTTPClient.Transport so audit/logging behaviour
//     stays consistent (every bridge call still emits the
//     `http POST 127.0.0.1:5555/cmd` audit line).
//
// The package-init pattern keeps the client-construction cost off
// the hot path.
var bridgeHTTPClient = newBridgeHTTPClient()

func newBridgeHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   bridgeTimeout(),
		Transport: core.HTTPClient.Transport,
	}
}

// bridgeTimeout returns the configured POST timeout. Defaults to
// 90s — comfortably above the typical 30-60s LinkedIn hydration
// window. Operators on slow networks bump it via
// SOCIAL_BRIDGE_TIMEOUT="180s" / "3m" / etc. Anything time.ParseDuration
// accepts works.
func bridgeTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("SOCIAL_BRIDGE_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		// Bare number → seconds (operator convenience).
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 90 * time.Second
}

func (c *Client) call(ctx context.Context, endpoint string, payload map[string]any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := bridgeHTTPClient.Do(req)
	if err != nil {
		// Distinguish "couldn't connect" from "connected then
		// timed out" so the operator-facing error gets the
		// right fix recipe. net.OpError + connection-refused-
		// shaped messages = real not-running. Anything with
		// "deadline exceeded" / "Client.Timeout" = bridge
		// accepted but stuck.
		msg := err.Error()
		if strings.Contains(msg, "deadline exceeded") ||
			strings.Contains(msg, "Client.Timeout") ||
			strings.Contains(msg, "context deadline") {
			return nil, fmt.Errorf("%w after %s: %v (LinkedIn / Medium / Substack page-load via the browser can be slow; bump SOCIAL_BRIDGE_TIMEOUT=180s if your pages need more headroom)",
				ErrBridgeTimeout, bridgeTimeout(), err)
		}
		return nil, fmt.Errorf("%w: %v", ErrBridgeUnreachable, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, ErrNoExtensionAttached
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bridge: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}
