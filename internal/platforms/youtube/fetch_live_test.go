//go:build live

package youtube

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// TestLiveYouTubeFetch fetches Rick Astley — "Never Gonna Give You Up"
// (dQw4w9WgXcQ). The most-stable YouTube URL on the planet. No API key
// is needed for metadata; the transcript may or may not come through
// depending on which provider (yt-dlp / innertube / kkdai) is available
// on the host, so we soft-warn when it's missing.
func TestLiveYouTubeFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", core.Options{IncludeComments: false})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if strings.TrimSpace(item.Title) == "" {
		t.Errorf("missing title")
	}
	if strings.TrimSpace(item.Author) == "" {
		t.Errorf("missing author")
	}
	if item.CanonicalID != "dQw4w9WgXcQ" {
		t.Errorf("canonical id: %q", item.CanonicalID)
	}
	if !strings.Contains(item.Content, "Transcript") {
		t.Logf("warning: no transcript section in content — yt-dlp/innertube/kkdai may all have failed for this video")
	}
	t.Logf("title=%q author=%q content_chars=%d", item.Title, item.Author, len(item.Content))
}
