//go:build live

package duckduckgo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// Live test — hits DDG's lite endpoint (no auth). DDG occasionally
// returns 0 results when its bot-detection trips; we soft-skip in that
// case rather than fail so CI stays green.
func TestLiveDDGSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	results, err := New().Search(ctx, "go programming language", core.SearchOptions{Max: 3})
	if err != nil {
		t.Skipf("ddg search skipped (likely rate-limited / bot-detected): %v", err)
	}
	if len(results) == 0 {
		t.Skipf("ddg returned 0 results — bot detection likely engaged")
	}
	first := results[0]
	if strings.TrimSpace(first.Title) == "" {
		t.Errorf("first result missing title")
	}
	if strings.TrimSpace(first.URL) == "" {
		t.Errorf("first result missing URL")
	}
	t.Logf("got %d results, first=%q (%s)", len(results), first.Title, first.URL)
}
