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

	"github.com/patrickdebois/social-skills/internal/core"
)

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
func (c *Client) GetHTML(ctx context.Context, target string, audit *core.AuditLogger) (htmlStr, finalURL, title string, err error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}

	audit.Logf("bridge: navigate %s", target)
	if _, err := c.call(ctx, endpoint, map[string]any{
		"command": "navigate",
		"url":     target,
	}); err != nil {
		return "", "", "", err
	}
	audit.Logf("bridge: get_html")
	body, err := c.call(ctx, endpoint, map[string]any{
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
