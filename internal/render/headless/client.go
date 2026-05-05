package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DaemonClient talks to a running headless daemon (`social-fetch
// headless start`). Cheap to construct — no resources held until
// Fetch is called. Used transparently by Fetcher when the daemon
// is reachable; falls through to in-process spawn otherwise.
type DaemonClient struct {
	BaseURL string        // e.g. http://127.0.0.1:5556
	HTTP    *http.Client  // override for tests; default has 90s Timeout
	Timeout time.Duration // per-request deadline (default 90s)
	// Token, when non-empty, is sent as Authorization: Bearer <token>
	// AND X-Daytona-Preview-Token: <token> on every request. Daytona's
	// signed proxy URLs accept either header form; we send both so the
	// same client reaches a self-hosted daemon (which may want plain
	// Bearer for its own auth) or a Daytona-tunneled one without
	// branching on URL shape.
	Token string
}

// NewDaemonClient builds a client pointed at the configured daemon
// URL. SOCIAL_FETCH_HEADLESS_DAEMON_URL overrides; default is the
// loopback address `headless start` listens on.
//
// SOCIAL_FETCH_HEADLESS_DAEMON_TOKEN, when set, attaches as a
// bearer + Daytona-preview header on every call — required when
// the URL points at a Daytona tunnel (`https://5556-<id>.daytonaproxy01.net`)
// or any other auth-gated reverse proxy.
func NewDaemonClient() *DaemonClient {
	url := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_HEADLESS_DAEMON_URL"))
	if url == "" {
		url = fmt.Sprintf("http://127.0.0.1:%d", DefaultDaemonPort)
	}
	return &DaemonClient{
		BaseURL: url,
		Timeout: 90 * time.Second,
		Token:   strings.TrimSpace(os.Getenv("SOCIAL_FETCH_HEADLESS_DAEMON_TOKEN")),
	}
}

// applyAuth adds bearer + Daytona-preview headers to req when
// the client has a token. No-op for local daemons.
func (c *DaemonClient) applyAuth(req *http.Request) {
	if c.Token == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Daytona-Preview-Token", c.Token)
}

// Reachable does a cheap GET /status to check whether the daemon is
// alive. Used by Fetcher.Fetch to decide between daemon-mode and
// in-process spawn. ~50 ms when up locally, ~connection-refused-fast
// when down. Cap at 1.5s — covers cross-region Daytona-tunnel
// latency (~500ms RTT EU↔US) plus TLS overhead on the proxy. The
// previous 250ms cap silently routed every Daytona-remote daemon
// call to in-process spawn because the probe always timed out.
func (c *DaemonClient) Reachable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.BaseURL+"/status", nil)
	if err != nil {
		return false
	}
	c.applyAuth(req)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// Fetch sends the URL to the daemon's /fetch endpoint and unwraps
// the response. Returns the same Result shape as in-process Fetch
// so callers can swap implementations without branching on origin.
func (c *DaemonClient) Fetch(ctx context.Context, url string) (*Result, error) {
	return c.FetchWithSettle(ctx, url, 0)
}

// FetchWithSettle is Fetch with a per-call settle override. settle
// of 0 falls back to the daemon's configured default (today: 2s).
// Used by the article fetcher's thin-content retry path —
// "retry the same URL with a longer hydration wait."
func (c *DaemonClient) FetchWithSettle(ctx context.Context, url string, settle time.Duration) (*Result, error) {
	body, err := json.Marshal(fetchRequest{URL: url})
	if err != nil {
		return nil, err
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	endpoint := c.BaseURL + "/fetch"
	if settle > 0 {
		endpoint += "?settle=" + settle.String()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("daemon: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	var fr fetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
		return nil, fmt.Errorf("daemon: decode: %w", err)
	}
	return &Result{
		HTML:     fr.HTML,
		FinalURL: fr.FinalURL,
		Engine:   fr.Engine + "+daemon",
	}, nil
}

// Screenshot POSTs to the daemon's /screenshot endpoint and returns
// the PNG bytes wrapped in a ScreenshotResult. settle of 0 falls back
// to the daemon's configured default; fullPage=true matches the
// in-process default.
func (c *DaemonClient) Screenshot(ctx context.Context, url string, settle time.Duration, fullPage bool) (*ScreenshotResult, error) {
	body, err := json.Marshal(screenshotRequest{URL: url})
	if err != nil {
		return nil, err
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	endpoint := c.BaseURL + "/screenshot"
	q := []string{}
	if !fullPage {
		q = append(q, "full_page=0")
	}
	if settle > 0 {
		q = append(q, "settle="+settle.String())
	}
	if len(q) > 0 {
		endpoint += "?" + strings.Join(q, "&")
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("daemon: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Body is plain-text on error (http.Error from the daemon).
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 1024 {
			msg = msg[:1024]
		}
		return nil, fmt.Errorf("daemon: HTTP %d: %s", resp.StatusCode, msg)
	}
	finalURL := resp.Header.Get("X-Final-URL")
	if finalURL == "" {
		finalURL = url
	}
	return &ScreenshotResult{
		PNG:      respBody,
		FinalURL: finalURL,
		Engine:   "chromedp+daemon",
	}, nil
}

// StatusResponse is the parsed /status JSON, exported so CLI
// commands can format it without re-decoding. Field names match
// the JSON tags on the daemon's internal statusResponse so tests
// and CLI use the same shape.
type StatusResponse = statusResponse

// SlotState is the per-slot snapshot exported for CLI rendering.
type SlotState = slotState

// FetchEntry is one row in the recent-fetch history.
type FetchEntry = fetchEntry

// Status hits GET /status and returns the parsed response. Used by
// the `social-fetch headless status` CLI subcommand.
func (c *DaemonClient) Status(ctx context.Context) (*StatusResponse, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.BaseURL+"/status", nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var s statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}
