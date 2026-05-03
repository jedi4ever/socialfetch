package serpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
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
	if _, err := p.Search(context.Background(), "anything", core.SearchOptions{Max: 10}); err == nil {
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

	got, err := p.Search(context.Background(), "anthropic claude", core.SearchOptions{Max: 5})
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

	_, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 5})
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("want API error, got %v", err)
	}
}

// TestSearchStartOffset confirms opts.Start propagates to SerpAPI's
// `start=` param verbatim — the agent passing start=20 should see
// `start=20` in the upstream request, not start=0 + a client-side
// slice. Without this, paging through page 2/3/4 would silently
// re-fetch page 1 from the cache.
func TestSearchStartOffset(t *testing.T) {
	var seenStart string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenStart = r.URL.Query().Get("start")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeJSON))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "k"

	if _, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 5, Start: 20}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if seenStart != "20" {
		t.Errorf("want start=20 in upstream request, got %q", seenStart)
	}
}

// TestSearchAutoPagination verifies that requesting Max=25 fans out
// across multiple SerpAPI calls (10/page) and concatenates the
// results. Each call asks for num=pageSize (10); the loop breaks
// either when we've hit Max, when an upstream page comes back short
// (< pageSize → end of results), or at maxPages.
//
// Fixture returns 10 hits on pages 1 and 2, then 5 hits on page 3
// (signaling end). We ask for Max=25 — should get 25 back across 3
// calls, with start offsets 0/10/20.
func TestSearchAutoPagination(t *testing.T) {
	var calls int
	var seenStarts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		seenStarts = append(seenStarts, r.URL.Query().Get("start"))
		w.Header().Set("Content-Type", "application/json")
		page := calls
		hitsThisPage := 10
		if page == 3 {
			hitsThisPage = 5 // short page = end of results
		}
		var b strings.Builder
		b.WriteString(`{"organic_results":[`)
		for i := 1; i <= hitsThisPage; i++ {
			if i > 1 {
				b.WriteString(",")
			}
			b.WriteString(`{"title":"P` + itoa(page) + `R` + itoa(i) +
				`","link":"https://e.com/p` + itoa(page) + `r` + itoa(i) + `","snippet":""}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "k"

	got, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 25})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Three pages: 10 + 10 + 5 = 25, all kept.
	if len(got) != 25 {
		t.Errorf("want 25 results, got %d", len(got))
	}
	if calls != 3 {
		t.Errorf("want 3 upstream calls, got %d (starts: %v)", calls, seenStarts)
	}
	if !strings.HasPrefix(got[0].Title, "P1R") {
		t.Errorf("first result not from page 1: %q", got[0].Title)
	}
	if !strings.HasPrefix(got[len(got)-1].Title, "P3R") {
		t.Errorf("last result not from page 3: %q", got[len(got)-1].Title)
	}
	// Start offsets advance by len(page). With 10/10/5 hits the
	// requests are start=0 / start=10 / start=20.
	wantStarts := []string{"", "10", "20"}
	for i, w := range wantStarts {
		if i < len(seenStarts) && seenStarts[i] != w {
			t.Errorf("call %d: start=%q, want %q", i+1, seenStarts[i], w)
		}
	}
}

// TestSearchAutoPaginationStopsOnShortPage verifies that the loop
// breaks when an upstream page returns fewer hits than the requested
// pageSize — that's our "we've hit the tail of the result set"
// signal. Stops the loop dead instead of paying for empty-page
// follow-ups.
func TestSearchAutoPaginationStopsOnShortPage(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		// First page returns only 2 hits (< pageSize=10) →
		// short-page heuristic should kick in immediately.
		_, _ = w.Write([]byte(`{"organic_results":[
            {"title":"A","link":"https://e.com/a","snippet":""},
            {"title":"B","link":"https://e.com/b","snippet":""}
        ]}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "k"

	got, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 results (short page = end), got %d", len(got))
	}
	if calls != 1 {
		t.Errorf("want 1 call (short page broke the loop), got %d", calls)
	}
}

// TestSearchAutoPaginationStopsOnEmptyPage covers the defensive
// branch: an upstream that returns a full page once and an empty
// page on the next call (happens when SerpAPI hits a hard cap).
func TestSearchAutoPaginationStopsOnEmptyPage(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			// Full pageSize=10 first → loop continues.
			var b strings.Builder
			b.WriteString(`{"organic_results":[`)
			for i := 0; i < 10; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(`{"title":"X","link":"https://e.com/x` + itoa(i) + `","snippet":""}`)
			}
			b.WriteString(`]}`)
			_, _ = w.Write([]byte(b.String()))
			return
		}
		// Page 2: empty → break path.
		_, _ = w.Write([]byte(`{"organic_results":[]}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "k"

	got, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 10 {
		t.Errorf("want 10 results, got %d", len(got))
	}
	if calls != 2 {
		t.Errorf("want 2 calls (full → empty → break), got %d", calls)
	}
}

// TestSearchAutoPaginationRespectsMaxPages caps the total upstream
// calls at maxPages even when the user asks for thousands of results.
// Without this guard a misconfigured Max=1_000_000 would silently
// charge SerpAPI for 100,000 pages.
func TestSearchAutoPaginationRespectsMaxPages(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		// Always return a full page so the loop never terminates
		// on "empty page" — only on maxPages.
		_, _ = w.Write([]byte(`{"organic_results":[
            {"title":"A","link":"https://e.com/a","snippet":""},
            {"title":"B","link":"https://e.com/b","snippet":""},
            {"title":"C","link":"https://e.com/c","snippet":""},
            {"title":"D","link":"https://e.com/d","snippet":""},
            {"title":"E","link":"https://e.com/e","snippet":""},
            {"title":"F","link":"https://e.com/f","snippet":""},
            {"title":"G","link":"https://e.com/g","snippet":""},
            {"title":"H","link":"https://e.com/h","snippet":""},
            {"title":"I","link":"https://e.com/i","snippet":""},
            {"title":"J","link":"https://e.com/j","snippet":""}
        ]}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "k"

	_, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 1_000_000})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if calls > maxPages {
		t.Errorf("upstream called %d times, want <= maxPages (%d)", calls, maxPages)
	}
}

// TestNewsProviderEmitsTBM confirms NewNewsProvider() flips the
// search to Google's News tab via tbm=nws and parses news_results
// (not organic_results) from the response.
func TestNewsProviderEmitsTBM(t *testing.T) {
	var seenTBM string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTBM = r.URL.Query().Get("tbm")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"news_results":[
            {"title":"Breaking","link":"https://news.example.com/1","snippet":"happened today","date":"1 hour ago","source":"NewsCo"}
        ]}`))
	}))
	defer srv.Close()

	p := NewNewsProvider()
	p.BaseURL = srv.URL
	p.Key = "k"

	if p.Name() != "serpapi-news" {
		t.Errorf("name = %q, want serpapi-news", p.Name())
	}
	got, err := p.Search(context.Background(), "anything", core.SearchOptions{Max: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if seenTBM != "nws" {
		t.Errorf("want tbm=nws in upstream request, got %q", seenTBM)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 result, got %d", len(got))
	}
	if got[0].Source != "serpapi-news" {
		t.Errorf("source = %q, want serpapi-news", got[0].Source)
	}
	// Snippet should carry the date + source prefix so the agent
	// can read freshness without a follow-up fetch.
	if !strings.Contains(got[0].Snippet, "1 hour ago") || !strings.Contains(got[0].Snippet, "NewsCo") {
		t.Errorf("snippet missing date/source: %q", got[0].Snippet)
	}
}

// TestSearchPassesGeoLocale confirms GL/HL/Location reach the
// upstream as gl/hl/location params. Both Provider-field and env-var
// shapes are exercised since the resolver prefers explicit fields.
func TestSearchPassesGeoLocale(t *testing.T) {
	var seen seenParams
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.gl = r.URL.Query().Get("gl")
		seen.hl = r.URL.Query().Get("hl")
		seen.location = r.URL.Query().Get("location")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeJSON))
	}))
	defer srv.Close()

	t.Run("via provider fields", func(t *testing.T) {
		seen = seenParams{}
		p := New()
		p.BaseURL = srv.URL
		p.Key = "k"
		p.GL = "fr"
		p.HL = "fr"
		p.Location = "Paris, France"
		_, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 1})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if seen.gl != "fr" || seen.hl != "fr" || seen.location != "Paris, France" {
			t.Errorf("got %+v, want gl=fr hl=fr location=Paris, France", seen)
		}
	})

	t.Run("via env vars", func(t *testing.T) {
		seen = seenParams{}
		t.Setenv("SERPAPI_GL", "uk")
		t.Setenv("SERPAPI_HL", "en")
		t.Setenv("SERPAPI_LOCATION", "London")
		p := New()
		p.BaseURL = srv.URL
		p.Key = "k"
		_, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 1})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if seen.gl != "uk" || seen.hl != "en" || seen.location != "London" {
			t.Errorf("got %+v, want gl=uk hl=en location=London", seen)
		}
	})

	t.Run("provider field beats env", func(t *testing.T) {
		seen = seenParams{}
		t.Setenv("SERPAPI_GL", "uk")
		p := New()
		p.BaseURL = srv.URL
		p.Key = "k"
		p.GL = "de"
		_, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 1})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if seen.gl != "de" {
			t.Errorf("gl = %q, want de (provider field should beat env)", seen.gl)
		}
	})
}

// Helpers — kept here rather than the package proper since they're
// test-only.

type seenParams struct{ gl, hl, location string }

// itoa avoids a strconv import for the small page-numbering use case.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
