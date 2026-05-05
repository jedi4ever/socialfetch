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

// TestJinaOptionsFromEnv covers the env-var override layer.
// SOCIAL_FETCH_JINA_* vars overlay DefaultJinaOptions; bad values
// fall through to the default rather than failing the build.
func TestJinaOptionsFromEnv(t *testing.T) {
	t.Run("all unset returns defaults", func(t *testing.T) {
		opts := JinaOptionsFromEnv()
		if opts != DefaultJinaOptions {
			t.Errorf("with no env, got %+v, want %+v", opts, DefaultJinaOptions)
		}
	})

	t.Run("engine override", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_JINA_ENGINE", "direct")
		if got := JinaOptionsFromEnv().Engine; got != "direct" {
			t.Errorf("Engine = %q, want direct", got)
		}
	})

	t.Run("no-cache toggle off", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_JINA_NO_CACHE", "false")
		if got := JinaOptionsFromEnv().NoCache; got != false {
			t.Errorf("NoCache = %v, want false", got)
		}
	})

	t.Run("format markdown", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_JINA_FORMAT", "markdown")
		if got := JinaOptionsFromEnv().Format; got != JinaFormatMarkdown {
			t.Errorf("Format = %q, want markdown", got)
		}
	})

	t.Run("timeout parse", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_JINA_TIMEOUT", "120s")
		if got := JinaOptionsFromEnv().Timeout; got != 120*time.Second {
			t.Errorf("Timeout = %v, want 120s", got)
		}
	})

	t.Run("timeout bad value falls back to default", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_JINA_TIMEOUT", "not-a-duration")
		if got := JinaOptionsFromEnv().Timeout; got != DefaultJinaOptions.Timeout {
			t.Errorf("Timeout = %v, want default %v", got, DefaultJinaOptions.Timeout)
		}
	})

	t.Run("model readerlm-v2", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_JINA_MODEL", "readerlm-v2")
		if got := JinaOptionsFromEnv().Model; got != "readerlm-v2" {
			t.Errorf("Model = %q, want readerlm-v2", got)
		}
	})
}

// TestJinaReader_ModelHeader verifies SOCIAL_FETCH_JINA_MODEL
// surfaces as the X-Respond-With header — the wire signal that
// switches Jina from heuristic readability to the LLM-based
// extractor.
func TestJinaReader_ModelHeader(t *testing.T) {
	t.Setenv("SOCIAL_FETCH_JINA_MODEL", "readerlm-v2")

	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"content": "ok"},
		})
	}))
	defer srv.Close()

	r := NewJinaReader()
	r.BaseURL = srv.URL + "/"
	if _, err := r.Read(context.Background(), "https://example.com/"); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := gotHeaders.Get("X-Respond-With"); got != "readerlm-v2" {
		t.Errorf("X-Respond-With = %q, want readerlm-v2", got)
	}
}

// TestJinaReader_ReadFull_JSON exercises the structured-output
// path in JSON mode: every JinaResult field should populate from
// the matching envelope key, and ```markdown fences around content
// should be stripped (readerlm-v2 wraps its output that way).
func TestJinaReader_ReadFull_JSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"title":         "  My Post  ",
				"description":   "A summary.",
				"url":           "https://example.com/canonical",
				"content":       "```markdown\n# Hello\n\nWorld\n```",
				"publishedTime": "2026-01-01T00:00:00Z",
			},
		})
	}))
	defer srv.Close()

	r := NewJinaReader()
	r.BaseURL = srv.URL + "/"
	res, err := r.ReadFull(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if res.Title != "My Post" {
		t.Errorf("Title = %q, want 'My Post'", res.Title)
	}
	if res.Description != "A summary." {
		t.Errorf("Description = %q", res.Description)
	}
	if res.URL != "https://example.com/canonical" {
		t.Errorf("URL = %q", res.URL)
	}
	if res.PublishedTime != "2026-01-01T00:00:00Z" {
		t.Errorf("PublishedTime = %q", res.PublishedTime)
	}
	if !strings.Contains(res.Content, "# Hello") {
		t.Errorf("Content = %q, want '# Hello'", res.Content)
	}
	if strings.Contains(res.Content, "```") {
		t.Errorf("Content still has fences: %q", res.Content)
	}
}

// TestJinaReader_ReadFull_Markdown exercises the same structured-
// output path against the markdown wire format. Jina's markdown
// preamble (Title: / URL Source: / Markdown Content:) gets parsed
// into the same JinaResult shape.
func TestJinaReader_ReadFull_Markdown(t *testing.T) {
	resp := "Title: My Post\n" +
		"URL Source: https://example.com/canonical\n" +
		"Published Time: 2026-01-01T00:00:00Z\n" +
		"Markdown Content:\n" +
		"# Hello\n\nWorld"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	opts := DefaultJinaOptions
	opts.Format = JinaFormatMarkdown
	r := NewJinaReaderWithOptions(opts)
	r.BaseURL = srv.URL + "/"

	res, err := r.ReadFull(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if res.Title != "My Post" {
		t.Errorf("Title = %q, want 'My Post'", res.Title)
	}
	if res.URL != "https://example.com/canonical" {
		t.Errorf("URL = %q", res.URL)
	}
	if res.PublishedTime != "2026-01-01T00:00:00Z" {
		t.Errorf("PublishedTime = %q", res.PublishedTime)
	}
	if !strings.HasPrefix(res.Content, "# Hello") {
		t.Errorf("Content = %q, want to start with '# Hello'", res.Content)
	}
}

// TestJinaReader_ReadFull_BarePassthrough — when the response has
// no preamble (raw markdown body, fixture-style), the parser
// should fall through with Title/URL empty and the body intact
// rather than erroring.
func TestJinaReader_ReadFull_BarePassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Just a body\n\nNo preamble."))
	}))
	defer srv.Close()

	opts := DefaultJinaOptions
	opts.Format = JinaFormatMarkdown
	r := NewJinaReaderWithOptions(opts)
	r.BaseURL = srv.URL + "/"

	res, err := r.ReadFull(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if res.Title != "" {
		t.Errorf("Title = %q, want empty (no preamble)", res.Title)
	}
	if !strings.Contains(res.Content, "Just a body") {
		t.Errorf("Content = %q", res.Content)
	}
}

// TestStripFences exercises the readerlm-v2 fence stripper directly.
// It should strip wrapping ```markdown … ``` blocks but leave
// mid-body inline code fences alone.
func TestStripFences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"markdown fence", "```markdown\n# Hi\nbody\n```", "# Hi\nbody"},
		{"plain fence", "```\n# Hi\n```", "# Hi"},
		{"trailing whitespace tolerated", "```md\n# Hi\n```\n\n", "# Hi"},
		{"no fence — passthrough", "# Hi\nbody", "# Hi\nbody"},
		{"opens but no close — passthrough", "```markdown\n# Hi", "```markdown\n# Hi"},
		{"mid-body fence stays", "real prose\n\n```go\nfn()\n```\n\nmore prose", "real prose\n\n```go\nfn()\n```\n\nmore prose"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripFences(c.in); got != c.want {
				t.Errorf("stripFences(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestJinaReader_ModelDefaultUnset confirms the X-Respond-With
// header is absent when no model is configured — the default Jina
// extractor stays the wire default, not readerlm-v2.
func TestJinaReader_ModelDefaultUnset(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"content": "ok"},
		})
	}))
	defer srv.Close()

	r := NewJinaReader()
	r.BaseURL = srv.URL + "/"
	if _, err := r.Read(context.Background(), "https://example.com/"); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := gotHeaders.Get("X-Respond-With"); got != "" {
		t.Errorf("X-Respond-With = %q, want empty (default)", got)
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
