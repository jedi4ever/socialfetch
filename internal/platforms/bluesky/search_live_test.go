//go:build live

package bluesky

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// TestLiveBlueskySearch exercises the authenticated searchPosts XRPC
// method. Skipped silently when BLUESKY_HANDLE / BLUESKY_APP_PASSWORD
// aren't set.
func TestLiveBlueskySearch(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("BLUESKY_HANDLE") == "" || os.Getenv("BLUESKY_APP_PASSWORD") == "" {
		t.Skip("BLUESKY_HANDLE / BLUESKY_APP_PASSWORD not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	results, err := NewSearchProvider().Search(ctx, "ai", core.SearchOptions{Max: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least one result, got 0")
	}
	first := results[0]
	if strings.TrimSpace(first.URL) == "" {
		t.Errorf("first result missing URL")
	}
	t.Logf("got %d results, first url=%s", len(results), first.URL)
}
