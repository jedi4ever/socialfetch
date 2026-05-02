package medium

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jedi4ever/socialfetch/internal/core"
)

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://medium.com/@alice/post", true},
		{"https://alice.medium.com/post", true},
		{"https://www.medium.com/x", true},
		{"https://substack.com/x", false},
		{"https://example.com/", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// Bridge happy path: when the bridge is reachable and returns ok+html,
// we extract via MediumExtractor and tag Extra["via"]="bridge".
func TestFetchViaBridge(t *testing.T) {
	const html = `<!DOCTYPE html>
<html><head>
  <meta property="og:title" content="A Great Post">
  <meta property="og:url" content="https://medium.com/@alice/a-great-post-abc123">
  <meta property="article:author" content="Alice">
  <meta property="article:published_time" content="2026-04-15T12:00:00Z">
  <link rel="canonical" href="https://medium.com/@alice/a-great-post-abc123">
</head><body><article><h1>A Great Post</h1><p>The body of the post.</p></article></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"ok","html":%q,"url":"https://medium.com/@alice/a-great-post-abc123","title":"A Great Post"}`, html)
	}))
	defer srv.Close()

	f := New()
	f.BridgeURL = srv.URL + "/cmd"

	item, err := f.Fetch(context.Background(),
		"https://medium.com/@alice/a-great-post-abc123",
		core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Source != "medium" {
		t.Errorf("source = %q, want medium", item.Source)
	}
	if got := item.Extra["via"]; got != "bridge" {
		t.Errorf("via = %v, want bridge", got)
	}
	if !strings.Contains(item.Content, "body of the post") {
		t.Errorf("body missing: %q", item.Content)
	}
}

// Bridge unreachable → fall back to direct HTTP. We point BridgeURL at
// a closed port so the request errors with a connection-refused, then
// hand control to the real fetch path.
func TestFetchFallsBackToHTTP(t *testing.T) {
	const html = `<!DOCTYPE html>
<html><head>
  <meta property="og:title" content="Free Excerpt">
  <link rel="canonical" href="https://medium.com/@alice/free-excerpt">
</head><body><article><p>Public excerpt only.</p></article></body></html>`

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, html)
	}))
	defer httpSrv.Close()

	f := New()
	// Bind to a port that isn't listening so /cmd connect refuses.
	f.BridgeURL = "http://127.0.0.1:1/cmd"

	item, err := f.Fetch(context.Background(), httpSrv.URL+"/post", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := item.Extra["via"]; got != "http" {
		t.Errorf("via = %v, want http (fallback)", got)
	}
	if !strings.Contains(item.Content, "Public excerpt") {
		t.Errorf("body missing: %q", item.Content)
	}
}
