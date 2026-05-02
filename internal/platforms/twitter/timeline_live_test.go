//go:build live

package twitter

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/dotenv"
)

// TestLiveTwitterTimeline exercises the X timeline provider, which
// wraps the recent-search endpoint with `from:<user>`. Requires the
// same X_API_KEY/SECRET as Search. Empty Children is tolerated when
// the user has no recent activity in X's 7-day window.
func TestLiveTwitterTimeline(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("X_API_KEY") == "" || os.Getenv("X_API_SECRET") == "" {
		t.Skip("X_API_KEY / X_API_SECRET not set — skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	item, err := NewXProvider(NewSearchProvider()).Fetch(ctx, "swyx", core.TimelineOptions{Max: 5})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(item.Children) == 0 {
		t.Logf("warning: 0 timeline children — @swyx may have no activity in the 7-day window")
	}
	t.Logf("got %d children", len(item.Children))
}
