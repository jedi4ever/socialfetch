package htmlmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestJinaReader_JSONMode verifies the default path: Accept:
// application/json + X-No-Cache + X-Engine: browser, response body is
// the {data:{content}} envelope, Read() returns just the content as
// a markdown string.
//
// Locks in the request-shaping headers — if the defaults drift away
// from "best quality, no cache, JSON envelope" without an
// intentional change, this test catches it.
func TestJinaReader_JSONMode(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":   200,
			"status": 20000,
			"data": map[string]any{
				"title":   "Hello",
				"url":     "https://example.com/",
				"content": "# Hello\n\nWorld",
			},
		})
	}))
	defer srv.Close()

	r := NewJinaReader()
	r.BaseURL = srv.URL + "/"
	md, err := r.Read(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(md, "# Hello") {
		t.Errorf("body = %q, want it to contain '# Hello'", md)
	}
	if got := gotHeaders.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}
	if got := gotHeaders.Get("X-Engine"); got != "browser" {
		t.Errorf("X-Engine = %q, want browser", got)
	}
	if got := gotHeaders.Get("X-No-Cache"); got != "true" {
		t.Errorf("X-No-Cache = %q, want true", got)
	}
}

// TestJinaReader_MarkdownModeOverride confirms callers can opt out
// of JSON mode via NewJinaReaderWithOptions — we still want to
// support the legacy raw-markdown wire shape for tests / specialised
// call sites that prefer it.
func TestJinaReader_MarkdownModeOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/markdown" {
			t.Errorf("Accept = %q, want text/markdown", got)
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Plain markdown body"))
	}))
	defer srv.Close()

	opts := DefaultJinaOptions
	opts.Format = JinaFormatMarkdown
	r := NewJinaReaderWithOptions(opts)
	r.BaseURL = srv.URL + "/"

	md, err := r.Read(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(md, "Plain markdown body") {
		t.Errorf("body = %q", md)
	}
}

// TestJinaReader_TimeoutDefaulted verifies NewJinaReaderWithOptions
// fills in Timeout when the caller passes a zero value — protects
// against regressions where a future caller sets only Engine and
// silently gets a no-timeout client.
func TestJinaReader_TimeoutDefaulted(t *testing.T) {
	r := NewJinaReaderWithOptions(JinaOptions{Engine: "direct"})
	if r.Client.Timeout != DefaultJinaOptions.Timeout {
		t.Errorf("Timeout = %v, want %v", r.Client.Timeout, DefaultJinaOptions.Timeout)
	}
}

// Compile-time guard: ensure Default* values stay non-zero so the
// defaults don't silently flip back to "no-cache off / engine
// unset" after a refactor.
var _ = func() bool {
	if DefaultJinaOptions.Engine == "" || !DefaultJinaOptions.NoCache ||
		DefaultJinaOptions.Format == "" || DefaultJinaOptions.Timeout == 0 {
		panic("DefaultJinaOptions has a zero field — see jina.go")
	}
	_ = time.Now
	return true
}()
