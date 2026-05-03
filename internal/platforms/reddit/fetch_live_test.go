//go:build live

package reddit

import (
	"context"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// TestLiveRedditPost hits a known stable post. Reddit may rate-limit
// unauthenticated requests aggressively; if this flakes, retry.
func TestLiveRedditPost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const url = "https://www.reddit.com/r/programming/comments/1aaaaaa/test/"
	// Use a real post: the AMA index post is stable.
	item, err := New().Fetch(ctx, "https://www.reddit.com/r/IAmA/comments/jqzj7/i_am_barack_obama_president_of_the_united_states/", core.Options{IncludeComments: false})
	if err != nil {
		t.Skipf("reddit live test skipped (likely rate-limited): %v\n%s", err, url)
	}
	if item.Author == "" {
		t.Errorf("missing author")
	}
	if item.Title == "" {
		t.Errorf("missing title")
	}
}
