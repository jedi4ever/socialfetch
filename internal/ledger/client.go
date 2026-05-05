package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/ledger/item"
	"github.com/jedi4ever/social-skills/internal/ledger/store"
)

// DaemonClient talks to a running ledger daemon (`social-ledger
// daemon start`). Cheap to construct — no resources held until a
// method is called. Used transparently by callers that probe for
// the daemon and fall back to direct store / subprocess when it's
// not reachable.
type DaemonClient struct {
	BaseURL string       // e.g. http://127.0.0.1:5557
	HTTP    *http.Client // override for tests; default has 30s Timeout
	Timeout time.Duration
}

// NewDaemonClient builds a client pointed at the configured daemon
// URL. SOCIAL_LEDGER_DAEMON_URL overrides; default is the loopback
// address `daemon start` listens on.
func NewDaemonClient() *DaemonClient {
	u := strings.TrimSpace(os.Getenv("SOCIAL_LEDGER_DAEMON_URL"))
	if u == "" {
		u = fmt.Sprintf("http://127.0.0.1:%d", DefaultDaemonPort)
	}
	return &DaemonClient{BaseURL: u, Timeout: 30 * time.Second}
}

// Disabled reports whether daemon-mode is suppressed via env.
// Used by callers that want to short-circuit even before the
// reachability probe (saves the 250 ms when an operator
// explicitly opted out).
func Disabled() bool {
	return strings.TrimSpace(os.Getenv("SOCIAL_LEDGER_DAEMON_DISABLE")) != ""
}

// Reachable does a cheap GET /status to check whether the daemon
// is alive. Used at the top of every caller path to decide
// daemon-mode vs direct/subprocess. ~50 ms when up, fast-fail when
// down — capped at 250 ms to bound overhead per call.
func (c *DaemonClient) Reachable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.BaseURL+"/status", nil)
	if err != nil {
		return false
	}
	client := c.client(250 * time.Millisecond)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// Status hits GET /status — used by the daemon-status CLI
// subcommand and as a richer probe target.
func (c *DaemonClient) Status(ctx context.Context) (*StatusResponse, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.BaseURL+"/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client(2 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var s StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Ingest posts items to /ingest. Returns the daemon's per-state
// counts (new/updated/unchanged) so the auto-ingest path can log
// what happened — same shape store.Ingest returns per item but
// summed for the whole batch.
func (c *DaemonClient) Ingest(ctx context.Context, items []item.Item) (*IngestResponse, error) {
	reqBody, err := json.Marshal(IngestRequest{Items: items})
	if err != nil {
		return nil, err
	}
	respBody, status, err := c.post(ctx, "/ingest", reqBody)
	if err != nil {
		return nil, err
	}
	if err := nonOKToError(status, respBody); err != nil {
		return nil, err
	}
	var out IngestResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Search runs an FTS query through the daemon and returns the
// matching items.
func (c *DaemonClient) Search(ctx context.Context, q string, limit int) ([]item.Item, error) {
	v := url.Values{}
	v.Set("q", q)
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	return c.getItems(ctx, "/search?"+v.Encode())
}

// Get returns a single item by URL. Returns (nil, nil) when the
// item isn't in the ledger — same convention as store.Get.
func (c *DaemonClient) Get(ctx context.Context, urlStr string) (*item.Item, error) {
	v := url.Values{}
	v.Set("url", urlStr)
	body, status, err := c.get(ctx, "/get?"+v.Encode())
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if err := nonOKToError(status, body); err != nil {
		return nil, err
	}
	var it item.Item
	if err := json.Unmarshal(body, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// List returns recent items matching opts.
func (c *DaemonClient) List(ctx context.Context, opts store.ListOpts) ([]item.Item, error) {
	v := url.Values{}
	if opts.Source != "" {
		v.Set("source", opts.Source)
	}
	if opts.Limit > 0 {
		v.Set("limit", strconv.Itoa(opts.Limit))
	}
	if !opts.Since.IsZero() {
		v.Set("since", strconv.FormatInt(opts.Since.Unix(), 10))
	}
	return c.getItems(ctx, "/list?"+v.Encode())
}

// Seen reports whether the URL has been ingested. Returns the
// metadata when seen, zero-value when not.
func (c *DaemonClient) Seen(ctx context.Context, urlStr string) (*SeenResponse, error) {
	v := url.Values{}
	v.Set("url", urlStr)
	body, status, err := c.get(ctx, "/seen?"+v.Encode())
	if err != nil {
		return nil, err
	}
	if err := nonOKToError(status, body); err != nil {
		return nil, err
	}
	var s SeenResponse
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Stats hits GET /stats and returns the parsed response.
func (c *DaemonClient) Stats(ctx context.Context) (*store.Stats, error) {
	body, status, err := c.get(ctx, "/stats")
	if err != nil {
		return nil, err
	}
	if err := nonOKToError(status, body); err != nil {
		return nil, err
	}
	var s store.Stats
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Forget deletes one item. Identifier is the URL (preferred) or
// the canonical Key. Returns true when something was deleted.
func (c *DaemonClient) Forget(ctx context.Context, urlOrKey string) (bool, error) {
	req := ForgetRequest{}
	// Heuristic: anything that looks like a URL (contains "://")
	// is treated as URL — daemon resolves URL→key server-side.
	// Without "://" we treat as a raw Key. This handles the
	// common http/https case AND synthetic schemes
	// (subscription://, citation://) used elsewhere.
	if strings.Contains(urlOrKey, "://") {
		req.URL = urlOrKey
	} else {
		req.Key = urlOrKey
	}
	reqBody, _ := json.Marshal(req)
	respBody, status, err := c.post(ctx, "/forget", reqBody)
	if err != nil {
		return false, err
	}
	if err := nonOKToError(status, respBody); err != nil {
		return false, err
	}
	var out ForgetResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return false, err
	}
	return out.Deleted, nil
}

// ContentURL returns the URL clients can GET to read just the
// markdown body of a stored item. Used by MCP in daemon mode to
// hand the agent a fetchable URL instead of a local file path.
//
// urlStr identifies the item by its source URL. The daemon
// resolves URL → key server-side.
func (c *DaemonClient) ContentURL(urlStr string) string {
	v := url.Values{}
	v.Set("url", urlStr)
	return c.BaseURL + "/content?" + v.Encode()
}

// ----- low-level HTTP helpers -----
//
// IMPORTANT: get / post read the response body fully inside the
// helper before returning. The earlier shape — returning a
// *http.Response that the caller streamed via json.NewDecoder —
// had a latent race: the helper used context.WithTimeout +
// `defer cancel()`, so the context was cancelled the moment the
// helper returned, and any reads from resp.Body after that
// point died with `context canceled`. For small responses that
// fit in the kernel's TCP receive buffer the read succeeded
// without re-checking the ctx; for larger responses (e.g. 14
// influencer rows ~ 30 KB) the read failed mid-stream. Buffering
// internally removes the foot-gun entirely.

func (c *DaemonClient) get(ctx context.Context, path string) (body []byte, status int, err error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.client(timeout).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (c *DaemonClient) post(ctx context.Context, path string, reqBody []byte) (body []byte, status int, err error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.BaseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client(timeout).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (c *DaemonClient) client(timeout time.Duration) *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: timeout}
}

func (c *DaemonClient) getItems(ctx context.Context, path string) ([]item.Item, error) {
	body, status, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := nonOKToError(status, body); err != nil {
		return nil, err
	}
	var items []item.Item
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// nonOKToError converts non-2xx responses into errors that
// surface the body — the daemon writes plain-text errors via
// http.Error so curl/log debugging stays readable.
//
// Takes (status, body) rather than *http.Response so callers can
// pre-read the body and let the underlying connection /
// timeout-context close before this runs (see the IMPORTANT
// note above the get/post helpers for why).
func nonOKToError(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	if len(body) > 1024 {
		body = body[:1024]
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return errors.New("ledger daemon: HTTP " + strconv.Itoa(status) + ": " + msg)
}
