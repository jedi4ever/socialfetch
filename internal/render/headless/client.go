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
}

// NewDaemonClient builds a client pointed at the configured daemon
// URL. SOCIAL_FETCH_HEADLESS_DAEMON_URL overrides; default is the
// loopback address `headless start` listens on.
func NewDaemonClient() *DaemonClient {
	url := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_HEADLESS_DAEMON_URL"))
	if url == "" {
		url = fmt.Sprintf("http://127.0.0.1:%d", DefaultDaemonPort)
	}
	return &DaemonClient{
		BaseURL: url,
		Timeout: 90 * time.Second,
	}
}

// Reachable does a cheap GET /status to check whether the daemon is
// alive. Used by Fetcher.Fetch to decide between daemon-mode and
// in-process spawn. ~50 ms when up, ~connection-refused-fast when
// down — we cap at 250 ms to bound the overhead on every fetch.
func (c *DaemonClient) Reachable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.BaseURL+"/status", nil)
	if err != nil {
		return false
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 250 * time.Millisecond}
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

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.BaseURL+"/fetch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

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

// Status hits GET /status and returns the parsed response. Used by
// the `social-fetch headless status` CLI subcommand.
func (c *DaemonClient) Status(ctx context.Context) (*statusResponse, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.BaseURL+"/status", nil)
	if err != nil {
		return nil, err
	}
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
