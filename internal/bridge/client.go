package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/patrickdebois/social-skills/internal/core"
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

// DefaultEndpoint is the local /cmd URL fetchers POST to.
const DefaultEndpoint = "http://127.0.0.1:5555/cmd"

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
// by callers to branch to a fallback.
var ErrBridgeUnreachable = errors.New("bridge: daemon not running")

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

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
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
