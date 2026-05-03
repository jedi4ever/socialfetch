package article

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
)

const mediumPage = `<!DOCTYPE html>
<html>
<head>
  <title>My Post – Medium</title>
  <meta property="og:title" content="My Post">
  <meta property="og:description" content="Short summary.">
  <meta property="og:image" content="https://miro.medium.com/hero.jpg">
  <meta property="og:url" content="https://medium.com/@alice/my-post-abc123">
  <meta property="og:site_name" content="Medium">
  <meta name="article:tag" content="golang, scraping">
  <meta name="article:published_time" content="2024-05-01T12:00:00Z">
  <link rel="canonical" href="https://medium.com/@alice/my-post-abc123">
  <script type="application/ld+json">
  {"@type":"Article","headline":"My Post","author":{"name":"Alice Smith"},"datePublished":"2024-05-01T12:00:00Z"}
  </script>
</head>
<body>
  <nav>menu</nav>
  <article>
    <h1>My Post</h1>
    <p>This is the <strong>first</strong> paragraph.</p>
    <p>And here is a <a href="https://example.com">link</a>.</p>
    <pre><code>some code</code></pre>
    <ul><li>one</li><li>two</li></ul>
  </article>
  <footer>copyright</footer>
</body>
</html>`

const genericPage = `<!DOCTYPE html>
<html><head><title>Plain Page</title></head>
<body><article><p>Just text.</p></article></body></html>`

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://medium.com/x", true},
		{"http://example.com/post", true},
		{"ftp://example.com/", false},
		{"news.ycombinator.com/item?id=1", false}, // no scheme
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestFetchMediumPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(mediumPage))
	}))
	defer srv.Close()

	// Override so classify still says "article" (because the test server
	// host isn't medium.com), but the metadata path is exercised fully.
	item, err := New().Fetch(context.Background(), srv.URL+"/post", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Title != "My Post" {
		t.Errorf("title: %q", item.Title)
	}
	if item.Author != "Alice Smith" {
		t.Errorf("author: %q", item.Author)
	}
	if !strings.Contains(item.Content, "first") {
		t.Errorf("content missing body: %q", item.Content)
	}
	if !strings.Contains(item.Content, "[link](https://example.com)") {
		t.Errorf("link not converted to markdown: %q", item.Content)
	}
	if strings.Contains(item.Content, "menu") || strings.Contains(item.Content, "copyright") {
		t.Errorf("nav/footer leaked into article body: %q", item.Content)
	}
	if len(item.Tags) != 2 || item.Tags[0] != "golang" {
		t.Errorf("tags: %+v", item.Tags)
	}
	if item.Published == nil || item.Published.Year() != 2024 {
		t.Errorf("published: %v", item.Published)
	}
	if len(item.Media) != 1 || item.Media[0].URL != "https://miro.medium.com/hero.jpg" {
		t.Errorf("media: %+v", item.Media)
	}
}

func TestFetchGenericPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(genericPage))
	}))
	defer srv.Close()

	item, err := New().Fetch(context.Background(), srv.URL+"/", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Title != "Plain Page" {
		t.Errorf("title: %q", item.Title)
	}
	if !strings.Contains(item.Content, "Just text.") {
		t.Errorf("content: %q", item.Content)
	}
}

// TestFetchFollowsRedirectAndPopulatesURL verifies that when the
// HTTP layer follows a redirect, the article fetcher records the
// post-redirect URL on item.URL — without that, Registry.Fetch
// can't stamp item.RequestURL with the original input, breaking
// the "seen via shortener" guarantee. We don't override og:url /
// canonical here so the redirect target IS the canonical URL.
func TestFetchFollowsRedirectAndPopulatesURL(t *testing.T) {
	// final server — the post-redirect destination. Returns a
	// minimal page WITHOUT og:url / link[rel=canonical] so the
	// extractor doesn't override our redirect URL.
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Destination</title></head><body><article><p>The real article.</p></article></body></html>`))
	}))
	defer final.Close()

	// short-link server — redirects to final.
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/article", http.StatusMovedPermanently)
	}))
	defer short.Close()

	rawURL := short.URL + "/abc"
	item, err := New().Fetch(context.Background(), rawURL, core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	wantURL := final.URL + "/article"
	if item.URL != wantURL {
		t.Errorf("item.URL = %q, want %q (post-redirect target)", item.URL, wantURL)
	}
}

// TestFetchPreservesURLWhenNoRedirect — the no-redirect case
// keeps item.URL == raw, so Registry.Fetch's auto-stamp leaves
// RequestURL empty and the JSON output has no `request_url` field.
func TestFetchPreservesURLWhenNoRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Direct</title></head><body><article><p>No redirect.</p></article></body></html>`))
	}))
	defer srv.Close()

	rawURL := srv.URL + "/page"
	item, err := New().Fetch(context.Background(), rawURL, core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.URL != rawURL {
		t.Errorf("item.URL = %q, want %q (unchanged when no redirect)", item.URL, rawURL)
	}
}

// silence unused-import lint when no helpers below need it.
var _ = url.Parse
