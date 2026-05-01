package tavily

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchRequiresKey(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "")
	if _, err := New().Search(context.Background(), "x", 5); err == nil {
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

	got, err := p.Search(context.Background(), "anthropic claude", 3)
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

func TestSearchTruncatesLongContent(t *testing.T) {
	long := strings.Repeat("x", 600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"t","url":"u","content":"` + long + `","score":1}]}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "any"

	got, err := p.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Snippet) <= 500 || !strings.HasSuffix(got[0].Snippet, "…") {
		t.Errorf("snippet not truncated: len=%d", len(got[0].Snippet))
	}
}
