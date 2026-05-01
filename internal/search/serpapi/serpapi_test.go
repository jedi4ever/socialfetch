package serpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/patrickdebois/social-skills/internal/search"
)

const fakeJSON = `{
  "organic_results": [
    {"title": "First", "link": "https://example.com/1", "snippet": "snip1"},
    {"title": "Second", "link": "https://example.com/2", "snippet": "snip2"}
  ]
}`

func TestSearchRequiresKey(t *testing.T) {
	t.Setenv("SERPAPI_KEY", "")
	p := New()
	if _, err := p.Search(context.Background(), "anything", search.Options{Max: 10}); err == nil {
		t.Errorf("expected missing-key error")
	}
}

func TestSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "api_key=secret") {
			t.Errorf("api_key not forwarded: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("q") != "anthropic claude" {
			t.Errorf("query missing: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeJSON))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "secret"

	got, err := p.Search(context.Background(), "anthropic claude", search.Options{Max: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].URL != "https://example.com/1" || got[0].Source != "serpapi" {
		t.Errorf("first: %+v", got[0])
	}
}

func TestSearchPropagatesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"Invalid API key"}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "bad"

	_, err := p.Search(context.Background(), "x", search.Options{Max: 5})
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("want API error, got %v", err)
	}
}
