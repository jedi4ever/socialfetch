package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// UserAgent identifies this client to remote servers. A real-browser-ish
// string keeps Reddit and others from serving a stub page, but we still
// announce who we are after it.
const UserAgent = "Mozilla/5.0 (compatible; socialfetch/0.1; +https://github.com/jedi4ever/socialfetch)"

// HTTPClient is the shared *http.Client every fetcher uses. Tests override
// the BaseURL fields on individual fetchers to point at httptest servers
// rather than swapping this client out.
//
// The transport is tuned for batch fetches: keep-alive on, generous per-host
// idle pool so 32 concurrent HN comment fetches reuse the same TCP/TLS
// session, HTTP/2 enabled. CheckRedirect captures the redirect chain so
// callers can see when a URL has moved.
var HTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &loggingRoundTripper{base: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		MaxConnsPerHost:       64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}},
	CheckRedirect: trackRedirects,
}

// loggingRoundTripper wraps every HTTP call and emits one audit line
// per request:
//
//	http GET news.ycombinator.com/item status=200 bytes=12345 in 234ms
//
// or on failure:
//
//	http GET host/path FAILED in 1.2s: dial tcp: i/o timeout
//
// The audit logger is read from the request's context (set via
// core.WithAudit). If no logger is attached, the transport is a
// straight pass-through.
//
// Bytes are counted by wrapping the response body so the actual
// transferred size is recorded — including streaming responses where
// Content-Length is missing or wrong. The log line fires when the
// body is closed (typical idiom: `defer resp.Body.Close()`).
//
// The URL is logged as host+path only — query strings often carry API
// keys / bearer tokens / signed URLs and must never reach the audit
// log.
type loggingRoundTripper struct{ base http.RoundTripper }

func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := l.base.RoundTrip(req)
	audit, _ := req.Context().Value(auditCtxKey).(*AuditLogger)
	if err != nil {
		if audit != nil {
			audit.Logf("http %s %s FAILED in %s: %v",
				req.Method, safeURL(req.URL.String()),
				time.Since(start).Round(time.Millisecond), err)
		}
		return resp, err
	}
	if audit == nil {
		return resp, nil
	}
	resp.Body = &countingBody{
		ReadCloser: resp.Body,
		onClose: func(n int64) {
			audit.Logf("http %s %s status=%d bytes=%d in %s",
				req.Method, safeURL(req.URL.String()),
				resp.StatusCode, n,
				time.Since(start).Round(time.Millisecond))
		},
	}
	return resp, nil
}

// safeURL returns host+path, stripping query strings so API keys
// passed via ?key=... don't end up in the audit log. Very long paths
// are truncated to keep one event readable on a single line.
func safeURL(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	// Drop scheme://; keep host + path.
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if len(raw) > 200 {
		raw = raw[:199] + "…"
	}
	return raw
}

// countingBody wraps an http.Response.Body to count bytes read and
// invoke onClose with the total when the caller closes the body. Pure
// pass-through otherwise.
type countingBody struct {
	io.ReadCloser
	n       int64
	closed  bool
	onClose func(int64)
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingBody) Close() error {
	err := c.ReadCloser.Close()
	if !c.closed && c.onClose != nil {
		c.closed = true
		c.onClose(c.n)
	}
	return err
}

// MovedError is returned when fetching a URL ultimately fails because the
// content has moved permanently and we received the new location. Callers
// can inspect Final to retry against it, or surface the move in audit logs.
type MovedError struct {
	From   string
	To     string
	Status int
}

func (e *MovedError) Error() string {
	return fmt.Sprintf("moved (%d): %s -> %s", e.Status, e.From, e.To)
}

// trackRedirects is set as http.Client.CheckRedirect. It records each hop
// on the *Request (via context) and stops after 10 hops like the default.
func trackRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if a, ok := req.Context().Value(auditCtxKey).(*AuditLogger); ok {
		from := via[len(via)-1].URL.String()
		a.Logf("redirect %s -> %s", from, req.URL.String())
	}
	return nil
}

type ctxKey int

const auditCtxKey ctxKey = 1

// WithAudit attaches an AuditLogger to ctx so trackRedirects can find it.
// Callers normally use Options.Audit; this helper is for advanced use.
func WithAudit(ctx context.Context, a *AuditLogger) context.Context {
	if a == nil {
		return ctx
	}
	return context.WithValue(ctx, auditCtxKey, a)
}

// AuditFromContext returns the AuditLogger attached via WithAudit, or
// nil if there isn't one. Useful from platform packages that want to
// emit a one-line note alongside the HTTP-level audit lines that the
// transport produces automatically — without needing to change the
// Options struct that gets passed in.
func AuditFromContext(ctx context.Context) *AuditLogger {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(auditCtxKey).(*AuditLogger)
	return a
}

// GetJSON fetches url and decodes its body into v. It surfaces non-2xx
// responses as an error so callers don't accidentally decode an HTML error
// page as JSON.
func GetJSON(ctx context.Context, url string, v any) error {
	body, err := getBody(ctx, url, "application/json")
	if err != nil {
		return err
	}
	defer body.Close()
	return json.NewDecoder(body).Decode(v)
}

// GetBytes fetches url and returns the raw body. Used for HTML pages and
// RSS feeds.
func GetBytes(ctx context.Context, url string) ([]byte, error) {
	body, err := getBody(ctx, url, "")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

func getBody(ctx context.Context, url, accept string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusPermanentRedirect:
			return nil, &MovedError{
				From: url, To: resp.Header.Get("Location"), Status: resp.StatusCode,
			}
		}
		return nil, fmt.Errorf("GET %s: HTTP %d %s", url, resp.StatusCode, snippet(body))
	}
	return resp.Body, nil
}

func snippet(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > 120 {
		b = b[:120]
	}
	return string(b)
}

// HTTPErrorBody reads up to 512 bytes from resp.Body and returns a
// trimmed, single-line snippet suitable for inclusion in an error
// message. Use it whenever an HTTP call returns a non-2xx so the user
// sees the API's actual reason ("rate limit exceeded", "invalid
// start_time", "expired key") instead of just the status code.
//
// The body is consumed and should not be read again by the caller.
func HTTPErrorBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return "(no body)"
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if len(raw) == 0 {
		return "(empty body)"
	}
	// Collapse whitespace so the message stays on one line.
	out := make([]byte, 0, len(raw))
	prevSpace := false
	for _, c := range raw {
		if c == '\n' || c == '\r' || c == '\t' {
			c = ' '
		}
		if c == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		out = append(out, c)
	}
	s := string(out)
	if len(s) > 256 {
		s = s[:256] + "…"
	}
	return s
}
