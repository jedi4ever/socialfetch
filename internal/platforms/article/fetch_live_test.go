//go:build live

package article

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// TestLiveArticleExampleDotCom uses example.com as the most stable HTTP
// page on the internet. We don't expect a real article, just successful
// fetch and metadata extraction.
func TestLiveArticleExampleDotCom(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://example.com/", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(strings.ToLower(item.Title), "example") {
		t.Errorf("unexpected title: %q", item.Title)
	}
}
