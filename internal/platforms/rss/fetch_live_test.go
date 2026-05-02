//go:build live

package rss

import (
	"context"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// TestLiveRSSXKCD fetches a real, well-known feed.
func TestLiveRSSXKCD(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://xkcd.com/rss.xml", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Title == "" {
		t.Errorf("missing feed title")
	}
	if len(item.Children) == 0 {
		t.Errorf("no entries returned")
	}
}
