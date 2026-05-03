//go:build live

package hackernews

import (
	"context"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// Live test — hits the real HN Firebase API. Run with:
//
//	go test -tags=live ./internal/sources/hackernews/...
//
// Item 1 is "Y Combinator", the very first story; it's stable.
func TestLiveHackerNewsItem1(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://news.ycombinator.com/item?id=1", core.Options{IncludeComments: false})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.CanonicalID != "1" {
		t.Errorf("id: %q", item.CanonicalID)
	}
	if item.Author == "" {
		t.Errorf("missing author")
	}
	if item.Published == nil {
		t.Errorf("missing published")
	}
}
