//go:build live

package arxiv

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// TestLiveArxivFetch hits the real export.arxiv.org Atom API. The
// "Attention Is All You Need" paper (1706.03762) is the canonical
// transformer paper — it isn't going anywhere.
func TestLiveArxivFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://arxiv.org/abs/1706.03762", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if strings.TrimSpace(item.Title) == "" {
		t.Errorf("missing title")
	}
	if strings.TrimSpace(item.Author) == "" {
		t.Errorf("missing author(s)")
	}
	if strings.TrimSpace(item.Summary) == "" {
		t.Logf("warning: empty summary — arxiv usually returns one")
	}
	t.Logf("title=%q authors=%q", item.Title, item.Author)
}
