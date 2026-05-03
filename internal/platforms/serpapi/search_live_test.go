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
