package perplexity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

func TestSearchPostsJSONAndDecodesResults(t *testing.T) {
	var gotBody searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header: %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "Hit One", "url": "https://example.com/1", "snippet": "first", "date": "2026-04-01"},
				{"title": "Hit Two", "url": "https://example.com/2", "snippet": "second", "last_updated": "2026-03-15"}
			],
			"id": "abc"
		}`))
	}))
	defer srv.Close()

	p := &SearchProvider{BaseURL: srv.URL, Key: "test-key"}
	results, err := p.Search(context.Background(), "rust async", core.SearchOptions{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotBody.Query != "rust async" {
		t.Errorf("query: %q", gotBody.Query)
	}
	if gotBody.MaxResults != 3 {
		t.Errorf("max_results: %d", gotBody.MaxResults)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Title != "Hit One" || results[0].URL != "https://example.com/1" {
		t.Errorf("first result: %+v", results[0])
	}
	if results[0].Source != "perplexity" {
		t.Errorf("source: %q", results[0].Source)
	}
	// First result has Date — should populate Published.
	if results[0].Published == nil || results[0].Published.Year() != 2026 {
		t.Errorf("Date didn't populate Published: %+v", results[0].Published)
	}
	// Second result has only LastUpdated — should fall back to that.
	if results[1].Published == nil || results[1].Published.Month() != 3 {
		t.Errorf("LastUpdated fallback didn't populate Published: %+v", results[1].Published)
	}
}

func TestSearchRequiresKey(t *testing.T) {
	t.Setenv("PERPLEXITY_API_KEY", "")
	t.Setenv("PPLX_API_KEY", "")
	p := &SearchProvider{BaseURL: "http://unused"}
	_, err := p.Search(context.Background(), "x", core.SearchOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "PERPLEXITY_API_KEY not set") {
		t.Errorf("unexpected: %v", err)
	}
}

func TestSearchEncodesDateFilters(t *testing.T) {
	var gotBody searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"id":"x"}`))
	}))
	defer srv.Close()

	after := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	p := &SearchProvider{BaseURL: srv.URL, Key: "k"}
	if _, err := p.Search(context.Background(), "q", core.SearchOptions{
		After:  &after,
		Before: &before,
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Perplexity expects MM/DD/YYYY (M/D/YYYY in Go's reference layout).
	if gotBody.SearchAfterDateFilter != "1/15/2026" {
		t.Errorf("after: %q", gotBody.SearchAfterDateFilter)
	}
	if gotBody.SearchBeforeDateFilter != "4/1/2026" {
		t.Errorf("before: %q", gotBody.SearchBeforeDateFilter)
	}
}

func TestSearchMergesIncludeAndExcludeDomains(t *testing.T) {
	var gotBody searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"id":"x"}`))
	}))
	defer srv.Close()

	p := &SearchProvider{BaseURL: srv.URL, Key: "k"}
	if _, err := p.Search(context.Background(), "q", core.SearchOptions{
		IncludeDomains: []string{"good.com", "also-good.com"},
		ExcludeDomains: []string{"bad.com"},
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []string{"good.com", "also-good.com", "-bad.com"}
	if len(gotBody.SearchDomainFilter) != len(want) {
		t.Fatalf("filter len: %d, want %d (%v)", len(gotBody.SearchDomainFilter), len(want), gotBody.SearchDomainFilter)
	}
	for i, w := range want {
		if gotBody.SearchDomainFilter[i] != w {
			t.Errorf("filter[%d] = %q, want %q", i, gotBody.SearchDomainFilter[i], w)
		}
	}
}

func TestSearchSurfacesErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "invalid query parameter"}`))
	}))
	defer srv.Close()

	p := &SearchProvider{BaseURL: srv.URL, Key: "k"}
	_, err := p.Search(context.Background(), "q", core.SearchOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid query parameter") {
		t.Errorf("error body not surfaced: %v", err)
	}
}
