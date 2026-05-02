package hackernews

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/patrickdebois/social-skills/internal/core"
)

// fakeAPI is a minimal stand-in for hacker-news.firebaseio.com. Tests register
// items by ID; the server serves them back as JSON exactly like Firebase.
func fakeAPI(t *testing.T, items map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/item/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v0/item/")
		id = strings.TrimSuffix(id, ".json")
		v, ok := items[id]
		if !ok {
			fmt.Fprint(w, "null")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
	return httptest.NewServer(mux)
}

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://news.ycombinator.com/item?id=12345", true},
		{"https://news.ycombinator.com/", true},
		{"12345", true},
		{"https://example.com/", false},
		{"https://medium.com/foo", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestExtractID(t *testing.T) {
	cases := map[string]string{
		"https://news.ycombinator.com/item?id=42": "42",
		"42": "42",
	}
	for in, want := range cases {
		got, err := extractID(in)
		if err != nil {
			t.Fatalf("extractID(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("extractID(%q) = %q, want %q", in, got, want)
		}
	}

	if _, err := extractID("https://example.com/"); err == nil {
		t.Errorf("expected error for non-HN url")
	}
}

func TestFetchStoryWithComments(t *testing.T) {
	items := map[string]any{
		"100": map[string]any{
			"id":          100,
			"type":        "story",
			"by":          "alice",
			"time":        1_700_000_000,
			"title":       "A great post",
			"text":        "<p>Hello, world.</p>",
			"url":         "https://example.com/post",
			"score":       42,
			"descendants": 2,
			"kids":        []int{200, 201},
		},
		"200": map[string]any{
			"id":   200,
			"type": "comment",
			"by":   "bob",
			"time": 1_700_000_100,
			"text": "First!",
			"kids": []int{300},
		},
		"201": map[string]any{
			"id":      201,
			"type":    "comment",
			"by":      "carol",
			"time":    1_700_000_200,
			"text":    "Deleted",
			"deleted": true,
		},
		"300": map[string]any{
			"id":   300,
			"type": "comment",
			"by":   "dave",
			"time": 1_700_000_300,
			"text": "Reply to bob",
		},
	}
	srv := fakeAPI(t, items)
	defer srv.Close()

	f := New()
	f.BaseURL = srv.URL + "/v0"

	item, err := f.Fetch(context.Background(), "https://news.ycombinator.com/item?id=100", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if item.Source != "hackernews" || item.Title != "A great post" {
		t.Errorf("unexpected item: %+v", item)
	}
	if item.Author != "alice" || item.Score != 42 {
		t.Errorf("unexpected author/score: %+v", item)
	}
	if got := len(item.Comments); got != 1 {
		t.Fatalf("want 1 surviving top-level comment (deleted dropped), got %d", got)
	}
	if item.Comments[0].Author != "bob" {
		t.Errorf("want bob first, got %q", item.Comments[0].Author)
	}
	if got := len(item.Comments[0].Replies); got != 1 {
		t.Errorf("want 1 reply to bob, got %d", got)
	}
	if item.Extra["comment_count"] != 2 {
		t.Errorf("want comment_count=2, got %v", item.Extra["comment_count"])
	}
}

func TestFetchUnknownID(t *testing.T) {
	srv := fakeAPI(t, nil)
	defer srv.Close()

	f := New()
	f.BaseURL = srv.URL + "/v0"

	if _, err := f.Fetch(context.Background(), "999", core.DefaultOptions()); err == nil {
		t.Errorf("expected error for unknown id")
	}
}
