//go:build live

package perplexity

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Live test — hits Perplexity's /search endpoint. Run with:
//
//	go test -tags=live -timeout 5m -run TestLivePerplexitySearch ./internal/platforms/perplexity/...
//
// Requires PERPLEXITY_API_KEY (or PPLX_API_KEY). Skipped silently if
// no key is available.
func TestLivePerplexitySearch(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("PERPLEXITY_API_KEY") == "" && os.Getenv("PPLX_API_KEY") == "" {
		t.Skip("PERPLEXITY_API_KEY / PPLX_API_KEY not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := NewSearchProvider().Search(ctx, "anthropic claude api", core.SearchOptions{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected at least one result, got 0")
	}
	first := results[0]
	if first.URL == "" {
		t.Errorf("first result missing URL")
	}
	if first.Source != "perplexity" {
		t.Errorf("source = %q, want perplexity", first.Source)
	}
	t.Logf("got %d results, first=%q (%s)", len(results), first.Title, first.URL)
}
