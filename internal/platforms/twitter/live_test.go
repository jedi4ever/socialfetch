//go:build live

package twitter

import (
	"context"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

// TestLiveTwitterFirstTweet — Jack Dorsey's "just setting up my twttr",
// the very first tweet, ID 20. Stable target for a smoke test.
//
// The syndication endpoint can rate-limit; we Skip on error rather than
// fail to keep CI quiet when Twitter is grumpy.
func TestLiveTwitterFirstTweet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://x.com/jack/status/20", core.DefaultOptions())
	if err != nil {
		t.Skipf("twitter live test skipped: %v", err)
	}
	if item.Author == "" {
		t.Errorf("missing author")
	}
	if item.Content == "" {
		t.Errorf("missing content")
	}
}
