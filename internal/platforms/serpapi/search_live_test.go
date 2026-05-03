//go:build live

package serpapi

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// TestLiveSerpAPISearch hits SerpAPI's google search engine via the
// search Provider (NOT the Asker — that uses google_ai_overview).
func TestLiveSerpAPISearch(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("SERPAPI_KEY") == "" {
		t.Skip("SERPAPI_KEY not set — skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results, err := New().Search(ctx, "site:anthropic.com claude", core.SearchOptions{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected at least one result, got 0")
	}
	t.Logf("got %d results", len(results))
}

// TestLiveSerpAPIPagination requests Max=20 to force ≥2 upstream
// pages and confirms results returned > pageSize — meaning the
// auto-page loop actually fanned out. Each iteration is a real
// charged SerpAPI request, so this test costs ~2 of the user's
// free-tier credits.
//
// Uses a result-rich query ("javascript") so page 1 is guaranteed
// to fill — narrow brand-name queries can return < pageSize on the
// first page and short-circuit the loop legitimately, which would
// produce a false-positive failure here.
func TestLiveSerpAPIPagination(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("SERPAPI_KEY") == "" {
		t.Skip("SERPAPI_KEY not set — skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// First check: page 1 has a full pageSize. If it doesn't, the
	// query itself is result-poor and pagination wouldn't fire even
	// with the best implementation — skip rather than fail.
	page1, err := New().Search(ctx, "javascript", core.SearchOptions{Max: 10})
	if err != nil {
		t.Fatalf("page 1 probe: %v", err)
	}
	if len(page1) < 10 {
		t.Skipf("query returned only %d on page 1 — can't exercise pagination", len(page1))
	}

	results, err := New().Search(ctx, "javascript", core.SearchOptions{Max: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) <= 10 {
		t.Errorf("expected >10 results from auto-pagination, got %d", len(results))
	}
	t.Logf("got %d results across multiple SerpAPI pages", len(results))
}

// TestLiveSerpAPIStartOffset exercises the explicit Start offset:
// asking for results 11-20 should give different hits than results
// 1-10 of the same query. Confirms the upstream `start=` param is
// being respected (not silently ignored).
func TestLiveSerpAPIStartOffset(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("SERPAPI_KEY") == "" {
		t.Skip("SERPAPI_KEY not set — skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	page1, err := New().Search(ctx, "anthropic claude", core.SearchOptions{Max: 5, Start: 0})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	page3, err := New().Search(ctx, "anthropic claude", core.SearchOptions{Max: 5, Start: 20})
	if err != nil {
		t.Fatalf("page 3 (start=20): %v", err)
	}
	if len(page1) == 0 || len(page3) == 0 {
		t.Skip("upstream returned 0 results — can't compare without data")
	}
	// Different start offsets should yield different lead URLs.
	// If they're identical, the upstream is ignoring `start=` (or
	// the query has fewer than 20 unique results, which is rare
	// for a brand-name query like this).
	if page1[0].URL == page3[0].URL {
		t.Errorf("start=0 and start=20 returned the same lead URL (%s) — paging not honored?", page1[0].URL)
	}
	t.Logf("page1[0]=%s, page3[0]=%s", page1[0].URL, page3[0].URL)
}

// TestLiveSerpAPINews exercises the serpapi-news variant: tbm=nws
// is added to the request, news_results parsed instead of organic_
// results, and the Source carries through as serpapi-news.
func TestLiveSerpAPINews(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("SERPAPI_KEY") == "" {
		t.Skip("SERPAPI_KEY not set — skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results, err := NewNewsProvider().Search(ctx, "anthropic", core.SearchOptions{Max: 5})
	if err != nil {
		t.Fatalf("News Search: %v", err)
	}
	if len(results) == 0 {
		t.Skip("no news results returned (transient or rate-limited)")
	}
	if results[0].Source != "serpapi-news" {
		t.Errorf("source = %q, want serpapi-news", results[0].Source)
	}
	t.Logf("got %d news results, first: %s", len(results), results[0].Title)
}
