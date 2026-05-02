//go:build live

package linkedin

import (
	"context"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// TestLiveLinkedInTimeline pulls the first page of patrickdebois'
// recent activity.
func TestLiveLinkedInTimeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	item, err := NewLinkedInProvider().Fetch(ctx, "patrickdebois", core.TimelineOptions{
		Kind: "posts",
		Max:  5,
	})
	if err != nil {
		if isBridgeEnvErr(err) {
			t.Skipf("bridge not available: %v", err)
		}
		t.Fatalf("Fetch: %v", err)
	}
	if len(item.Children) == 0 {
		t.Logf("warning: timeline returned 0 children — LinkedIn may have served an empty first page")
	}
	t.Logf("got %d items in timeline", len(item.Children))
}
