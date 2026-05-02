package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/search"
)

func TestSearchRequiresKey(t *testing.T) {
	t.Setenv("YOUTUBE_API_KEY", "")
	if _, err := NewSearchProvider().Search(context.Background(), "x", search.Options{Max: 5}); err == nil {
		t.Errorf("expected missing-key error")
	}
}

// Happy path: forwards key/query/maxResults and decodes the response.
func TestSearchHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("key") != "K" {
			t.Errorf("key not forwarded: %q", q.Get("key"))
		}
		if q.Get("q") != "vibe coding" {
			t.Errorf("q = %q", q.Get("q"))
		}
		if q.Get("type") != "video" {
			t.Errorf("type should always be video, got %q", q.Get("type"))
		}
		if q.Get("maxResults") != "3" {
			t.Errorf("maxResults = %q", q.Get("maxResults"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id": map[string]any{"videoId": "abc12345678"},
					"snippet": map[string]any{
						"title":        "Vibe coding 101",
						"description":  "An overview.",
						"channelTitle": "AI Channel",
						"publishedAt":  "2026-04-15T12:00:00Z",
					},
				},
				{
					// id without videoId — should be filtered out
					"id":      map[string]any{},
					"snippet": map[string]any{"title": "skip me"},
				},
			},
		})
	}))
	defer srv.Close()

	p := NewSearchProvider()
	p.BaseURL = srv.URL
	p.Key = "K"

	got, err := p.Search(context.Background(), "vibe coding", search.Options{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 result, got %d: %+v", len(got), got)
	}
	if got[0].URL != "https://www.youtube.com/watch?v=abc12345678" {
		t.Errorf("URL = %q", got[0].URL)
	}
	if !strings.Contains(got[0].Snippet, "AI Channel") || !strings.Contains(got[0].Snippet, "An overview") {
		t.Errorf("snippet should combine channel + description, got %q", got[0].Snippet)
	}
	if got[0].Published == nil {
		t.Errorf("Published should be parsed")
	}
}

// When After is set, order is forced to "date" and publishedAfter is
// passed as RFC3339.
func TestSearchAfterForcesDateOrder(t *testing.T) {
	var seenOrder, seenAfter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenOrder = r.URL.Query().Get("order")
		seenAfter = r.URL.Query().Get("publishedAfter")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer srv.Close()

	p := NewSearchProvider()
	p.BaseURL = srv.URL
	p.Key = "K"

	after := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if _, err := p.Search(context.Background(), "x", search.Options{Max: 5, After: &after}); err != nil {
		t.Fatal(err)
	}
	if seenOrder != "date" {
		t.Errorf("order = %q, want date when After is set", seenOrder)
	}
	if seenAfter != "2026-04-01T00:00:00Z" {
		t.Errorf("publishedAfter = %q", seenAfter)
	}
}

// 403 surfaces a useful message about quota / restrictions.
func TestSearch403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", 403)
	}))
	defer srv.Close()
	p := NewSearchProvider()
	p.BaseURL = srv.URL
	p.Key = "K"
	_, err := p.Search(context.Background(), "x", search.Options{Max: 5})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("want 403 error, got %v", err)
	}
}
