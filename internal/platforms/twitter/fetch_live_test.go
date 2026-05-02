//go:build live

package twitter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/dotenv"
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

// TestLiveTwitterLongFormArticle exercises the V2 `article` field
// expansion. When a tweet wraps an X long-form article (the new
// /i/article/<id> feature), the parent tweet's `text` field is just
// the article URL — the actual prose lives in `data.article.plain_text`,
// which our v2 fetcher now requests via `tweet.fields=article`.
//
// Asserts the article body is inlined as Content (not the article
// URL) and the title comes from `data.article.title` rather than
// the truncated tweet text. Skips if X creds aren't configured.
//
// Sample URL is the post that surfaced the gap originally — Trevin's
// "Compound Engineering v3" article.
func TestLiveTwitterLongFormArticle(t *testing.T) {
	dotenv.LoadAuto()
	if _, ok := FromEnv(); !ok {
		t.Skip("X_API_KEY / X_API_SECRET not set — skipping (long-form article needs v2 API)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://x.com/trevin/status/2047066108763770998", core.Options{IncludeComments: false})
	if err != nil {
		t.Skipf("twitter live test skipped: %v", err)
	}
	if item.Kind != "article" {
		t.Errorf("kind = %q, want article", item.Kind)
	}
	if !strings.Contains(item.Title, "Compound Engineering") {
		t.Errorf("title should be article title, got %q", item.Title)
	}
	if strings.HasPrefix(strings.TrimSpace(item.Content), "http") &&
		strings.Contains(item.Content, "x.com/i/article/") &&
		len(item.Content) < 200 {
		t.Errorf("content is just the article URL — article expansion didn't engage: %q", item.Content)
	}
	if len(item.Content) < 1000 {
		t.Errorf("content too short for an article: %d chars", len(item.Content))
	}
	t.Logf("long-form article: title=%q content_chars=%d", item.Title, len(item.Content))
}
