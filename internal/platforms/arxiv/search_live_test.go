//go:build live

package arxiv

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// TestLiveArxivSearch hits the public Atom search endpoint. "transformer"
// is broad enough that arxiv will always have many results.
func TestLiveArxivSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	results, err := NewSearchProvider().Search(ctx, "transformer", core.SearchOptions{Max: 3})
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
	if strings.TrimSpace(first.URL) == "" {
		t.Errorf("first result missing URL")
	}
	t.Logf("got %d results, first=%q (%s)", len(results), first.Title, first.URL)
}
