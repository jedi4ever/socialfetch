//go:build live

package github

import (
	"context"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

// TestLiveGitHubGoRepo hits the real GitHub API. golang/go is stable.
// Set GITHUB_TOKEN to avoid hitting the unauthenticated rate limit.
func TestLiveGitHubGoRepo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://github.com/golang/go", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Title != "golang/go" {
		t.Errorf("title: %q", item.Title)
	}
	if item.Score < 1000 {
		t.Errorf("expected lots of stars, got %d", item.Score)
	}
	if item.Content == "" {
		t.Errorf("missing README content")
	}
}
