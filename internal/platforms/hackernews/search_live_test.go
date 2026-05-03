//go:build live

package hackernews

import (
	"context"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// TestLiveHackerNewsSearch hits the Algolia HN search index. No auth.
func TestLiveHackerNewsSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results, err := NewSearchProvider().Search(ctx, "rust async", core.SearchOptions{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected at least one result, got 0")
	}
	t.Logf("got %d results", len(results))
}
