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

// TestLiveTwitterSearch hits X v2 /tweets/search/recent. Requires
// X_API_KEY + X_API_SECRET. The recent-search endpoint caps history
// at 7 days; we explicitly pin the After window inside that to avoid
// HTTP 400. Zero results is a soft-warn (X recent index is sparse for
// many queries), not a fail.
func TestLiveTwitterSearch(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("X_API_KEY") == "" || os.Getenv("X_API_SECRET") == "" {
		t.Skip("X_API_KEY / X_API_SECRET not set — skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	after := time.Now().Add(-24 * time.Hour)
	results, err := NewSearchProvider().Search(ctx, "anthropic claude", core.SearchOptions{
		Max:   3,
		After: &after,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Logf("warning: 0 results — X recent index can be sparse for narrow queries")
	}
	t.Logf("got %d results", len(results))
}
