//go:build live

package brave

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Live test — hits Brave Search's web/search endpoint. Run with:
//
//	go test -tags=live -timeout 5m ./internal/platforms/brave/...
//
// Requires BRAVE_API_KEY. Skipped silently if missing.
func TestLiveBraveSearch(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("BRAVE_API_KEY") == "" {
		t.Skip("BRAVE_API_KEY not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	results, err := New().Search(ctx, "rust programming", core.SearchOptions{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least one result, got 0")
	}
	first := results[0]
	if strings.TrimSpace(first.Title) == "" {
		t.Errorf("first result missing title")
	}
	if strings.TrimSpace(first.URL) == "" {
		t.Errorf("first result missing URL")
	}
	t.Logf("got %d results, first=%q (%s)", len(results), first.Title, first.URL)
}
