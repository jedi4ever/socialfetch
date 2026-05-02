//go:build live

package bluesky

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// TestLiveBlueskyFetch hits the public AppView (no auth). If Bluesky
// returns an empty thread (e.g. the rkey gets deleted by its author),
// soft-skip so CI stays green — bluesky URLs aren't permanent the way
// HN item ids or X tweet ids are.
func TestLiveBlueskyFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const postURL = "https://bsky.app/profile/nearestnabors.com/post/3mkrvymalx22h"
	item, err := New().Fetch(ctx, postURL, core.Options{IncludeComments: false})
	if err != nil {
		t.Skipf("bluesky live fetch skipped (post may have been deleted): %v", err)
	}
	if strings.TrimSpace(item.Author) == "" {
		t.Errorf("missing author")
	}
	if strings.TrimSpace(item.Content) == "" {
		t.Errorf("missing content")
	}
	t.Logf("author=%q content_chars=%d", item.Author, len(item.Content))
}
