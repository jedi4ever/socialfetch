//go:build live

package reddit

import (
	"context"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// TestLiveRedditSearch hits Reddit's unauthenticated JSON search.
// Same rate-limit caveat as TestLiveRedditPost — skip on error rather
// than fail so flaky CI stays quiet.
func TestLiveRedditSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results, err := NewSearchProvider().Search(ctx, "golang", core.SearchOptions{Max: 3})
	if err != nil {
		t.Skipf("reddit search skipped (likely rate-limited): %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected at least one result, got 0")
	}
	t.Logf("got %d results", len(results))
}
