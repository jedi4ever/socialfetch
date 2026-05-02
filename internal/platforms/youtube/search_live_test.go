//go:build live

package youtube

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/dotenv"
)

// TestLiveYouTubeSearch hits the YouTube Data API v3 search.list
// endpoint. Skipped silently when YOUTUBE_API_KEY isn't set.
func TestLiveYouTubeSearch(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("YOUTUBE_API_KEY") == "" {
		t.Skip("YOUTUBE_API_KEY not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	results, err := NewSearchProvider().Search(ctx, "claude api", core.SearchOptions{Max: 3})
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
	if !strings.Contains(first.URL, "youtube.com/watch?v=") {
		t.Errorf("unexpected URL shape: %q", first.URL)
	}
	t.Logf("got %d results, first=%q (%s)", len(results), first.Title, first.URL)
}
