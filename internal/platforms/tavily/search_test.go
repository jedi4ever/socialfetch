package tavily

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

func TestSearchRequiresKey(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "")
	if _, err := New().Search(context.Background(), "x", core.SearchOptions{Max: 5}); err == nil {
		t.Errorf("expected missing-key error")
	}
}

func TestSearchPostsJSONAndDecodesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing JSON content-type")
		}
		body, _ := io.ReadAll(r.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		if req.APIKey != "secret" {
			t.Errorf("api_key not forwarded: %q", req.APIKey)
		}
		if req.Query != "anthropic claude" {
			t.Errorf("query: %q", req.Query)
		}
		if req.MaxResults != 3 {
			t.Errorf("max_results: %d", req.MaxResults)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"answer": "synthesized",
			"results": [
				{"title": "First", "url": "https://example.com/1", "content": "snip 1", "score": 0.91, "published_date": "2026-04-01"},
				{"title": "Second", "url": "https://example.com/2", "content": "snip 2", "score": 0.78}
			]
		}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "secret"

	got, err := p.Search(context.Background(), "anthropic claude", core.SearchOptions{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].URL != "https://example.com/1" || got[0].Source != "tavily" {
		t.Errorf("first: %+v", got[0])
	}
}

// When the caller passes opts.After, results outside the window must be
// dropped by the client-side post-filter using published_date. The topic
// stays "general" — switching to "news" tanks recall on non-news queries.
func TestSearchPostFiltersByPublishedDate(t *testing.T) {
	var seenTopic string
	var seenDays int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req request
		_ = json.Unmarshal(body, &req)
		seenTopic = req.Topic
		seenDays = req.Days
		// One in-window, one out-of-window, one without a date.
		_, _ = w.Write([]byte(`{
			"results":[
				{"title":"Fresh","url":"https://example.com/fresh","content":"in window","score":1,"published_date":"` +
			time.Now().UTC().Format("2006-01-02") + `"},
				{"title":"Old","url":"https://example.com/old","content":"too old","score":1,"published_date":"2020-01-01"},
				{"title":"Undated","url":"https://example.com/undated","content":"no date","score":1}
			]
		}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "k"

	after := time.Now().AddDate(0, 0, -7)
	got, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 5, After: &after})
	if err != nil {
		t.Fatal(err)
	}
	if seenTopic != "general" {
		t.Errorf("topic should stay general, got %q", seenTopic)
	}
	if seenDays <= 0 {
		t.Errorf("days hint should still be sent: %d", seenDays)
	}
	// Old result dropped; fresh + undated kept (we can't prove undated is stale).
	if len(got) != 2 {
		t.Fatalf("want 2 results (fresh + undated), got %d: %+v", len(got), got)
	}
	if got[0].URL != "https://example.com/fresh" || got[0].Published == nil {
		t.Errorf("fresh result missing or no Published: %+v", got[0])
	}
	if got[1].URL != "https://example.com/undated" {
		t.Errorf("undated result not kept: %+v", got[1])
	}
}

func TestSearchTruncatesLongContent(t *testing.T) {
	long := strings.Repeat("x", 600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"t","url":"u","content":"` + long + `","score":1}]}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "any"

	got, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Snippet) <= 500 || !strings.HasSuffix(got[0].Snippet, "…") {
		t.Errorf("snippet not truncated: len=%d", len(got[0].Snippet))
	}
}
