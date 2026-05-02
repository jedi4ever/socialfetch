//go:build live

package tavily

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/dotenv"
)

// TestLiveTavilySearch hits Tavily's /search endpoint via the search
// Provider (NOT the Asker — this exercises the SearchProvider wiring).
func TestLiveTavilySearch(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("TAVILY_API_KEY") == "" {
		t.Skip("TAVILY_API_KEY not set — skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results, err := New().Search(ctx, "rust async", core.SearchOptions{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected at least one result, got 0")
	}
	t.Logf("got %d results", len(results))
}
