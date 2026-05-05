//go:build live

package article

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
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

// TestLiveArticleFetchMediaImageRich hits a known image-rich blog
// (the user-reported milvus.io article that motivated the redirect-
// loop fix in v0.10.13 — also has multiple inline diagrams) and
// asserts body-image extraction surfaces at least one image. Stable
// enough as a fixture: blogs of this shape rarely strip imagery.
//
// Note: this URL goes through the Jina-Reader fallback path because
// milvus.io 404s our HTTP UA — Jina returns clean markdown which
// won't have the original <img> tags, so this test is really
// asserting "at least the og:image hero gets through" via
// BaseFromPage. Use a different fixture if Jina markdown becomes
// the test target later.
func TestLiveArticleFetchMediaImageRich(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const postURL = "https://newsletter.armand.so/p/the-five-pillars-of-context-engineering"
	item, err := New().Fetch(ctx, postURL, core.DefaultOptions())
	if err != nil {
		t.Skipf("article live fetch skipped: %v", err)
	}
	if len(item.Media) == 0 {
		t.Errorf("expected at least the og:image hero in Media, got 0")
	}
	for i, m := range item.Media {
		t.Logf("media[%d] type=%s url=%s alt=%q", i, m.Type, m.URL, m.Alt)
	}
}

// TestLiveArticleFetchHeadless forces the chromedp headless
// transport against example.com (boring but stable) — verifies the
// transport is wired into the article chain end-to-end. The
// generic article extractor handles chromedp's DOM the same way it
// handles the http path, so we just check title + "example" in the
// body.
func TestLiveArticleFetchHeadless(t *testing.T) {
	t.Setenv("SOCIAL_FETCH_CHAIN_ARTICLE", "headless")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://example.com/", core.DefaultOptions())
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("chrome not installed: %v", err)
		}
		t.Fatalf("Fetch via headless: %v", err)
	}
	if via, _ := item.Extra["via"].(string); via != "headless" {
		t.Errorf("Extra[via] = %q, want headless", via)
	}
	if !strings.Contains(strings.ToLower(item.Title), "example") {
		t.Errorf("title = %q, want it to contain 'example'", item.Title)
	}
	t.Logf("article headless: title=%q content_chars=%d engine=%v",
		item.Title, len(item.Content), item.Extra["engine"])
}
