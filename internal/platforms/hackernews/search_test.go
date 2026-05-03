package hackernews

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
)

const fakeJSON = `{
  "hits": [
    {
      "objectID": "100",
      "title": "Show HN: Cool thing",
      "url": "https://example.com/cool",
      "author": "alice",
      "points": 250,
      "num_comments": 42,
      "created_at": "2026-04-01T12:00:00Z",
      "_tags": ["story", "show_hn", "author_alice"]
    },
    {
      "objectID": "101",
      "title": "Discussion only",
      "url": "",
      "author": "bob",
      "points": 99,
      "num_comments": 7,
      "story_text": "Body of an Ask HN with no external link.",
      "_tags": ["story", "ask_hn"]
    }
  ]
}`

func TestSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "rust async" {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("hitsPerPage") != "5" {
			t.Errorf("hitsPerPage: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("tags") != "story" {
			t.Errorf("tags: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeJSON))
	}))
	defer srv.Close()

	p := NewSearchProvider()
	p.BaseURL = srv.URL

	got, err := p.Search(context.Background(), "rust async", core.SearchOptions{Max: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if !strings.Contains(got[0].Title, "Show HN: Cool thing") {
		t.Errorf("first title: %q", got[0].Title)
	}
	if !strings.Contains(got[0].Title, "250 points") {
		t.Errorf("points not in title: %q", got[0].Title)
	}
	if got[0].URL != "https://example.com/cool" {
		t.Errorf("first URL: %q", got[0].URL)
	}
	// Story without external URL should fall back to the HN discussion link.
	if got[1].URL != "https://news.ycombinator.com/item?id=101" {
		t.Errorf("second URL: %q", got[1].URL)
	}
	if got[1].Snippet != "Body of an Ask HN with no external link." {
		t.Errorf("second snippet: %q", got[1].Snippet)
	}
}

func TestSearchByDateRewritesPath(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		_, _ = w.Write([]byte(`{"hits":[]}`))
	}))
	defer srv.Close()

	p := NewSearchProvider()
	p.BaseURL = srv.URL + "/api/v1/search"
	p.SortByDate = true

	if _, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 5}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.HasSuffix(hitPath, "/search_by_date") {
		t.Errorf("expected /search_by_date suffix, got %q", hitPath)
	}
}
