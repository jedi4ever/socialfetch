//go:build live

package medium

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// TestLiveMediumFetch hits a real Medium post. The fetcher is
// dual-path (bridge first, direct HTTP fallback) so the test works
// whether or not the bridge is running. The bridge can be slow to
// settle, so we use a 60s timeout.
//
// If Medium serves a paywall preview the body may be short; we
// soft-warn rather than fail in that case.
func TestLiveMediumFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const postURL = "https://medium.com/@patrickdebois/the-three-pillars-of-the-context-engineering-lifecycle-cdlc-43f1c0066b4f"
	item, err := New().Fetch(ctx, postURL, core.DefaultOptions())
	if err != nil {
		t.Skipf("medium live fetch skipped (bridge + http both failed): %v", err)
	}
	if strings.TrimSpace(item.Title) == "" {
		t.Errorf("missing title")
	}
	if strings.TrimSpace(item.Content) == "" {
		t.Errorf("missing content")
	} else if len(item.Content) < 200 {
		t.Logf("warning: content is short (%d chars) — Medium may have served only a paywall excerpt", len(item.Content))
	}
	t.Logf("title=%q content_chars=%d via=%v", item.Title, len(item.Content), item.Extra["via"])
}

// TestLiveMediumFetchMedia confirms body-image extraction populates
// item.Media against a real Medium post (the Phoenix Principle
// manifesto — image-rich, public, expected to stay up). Asserts
// `len(Media) > 0` rather than a fixed count since editors can
// change image content post-publish.
func TestLiveMediumFetchMedia(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const postURL = "https://medium.com/@bergel/the-phoenix-principle-a-manifesto-for-programmers-in-the-ai-age-ca63317c5ebc"
	item, err := New().Fetch(ctx, postURL, core.DefaultOptions())
	if err != nil {
		t.Skipf("medium live fetch skipped: %v", err)
	}
	if len(item.Media) == 0 {
		t.Errorf("expected at least the og:image hero in Media, got 0")
	}
	for i, m := range item.Media {
		t.Logf("media[%d] type=%s url=%s alt=%q", i, m.Type, m.URL, m.Alt)
	}
}

// TestLiveMediumFetchHeadless forces the chromedp headless transport
// via SOCIAL_FETCH_CHAIN_MEDIUM=headless. Verifies the per-transport
// extractor (extractHeadless) walks the chromedp DOM and produces a
// non-empty body.
//
// Medium's anti-bot occasionally degrades chromedp responses to a
// shell with no <article> element — when that happens the extractor
// falls through to og:description (Summary) so we still return SOME
// body. Test tolerates the degraded case (logs a warning) but fails
// hard on missing title (og:title is server-rendered and should
// always be there).
func TestLiveMediumFetchHeadless(t *testing.T) {
	t.Setenv("SOCIAL_FETCH_CHAIN_MEDIUM", "headless")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const postURL = "https://medium.com/@bergel/the-phoenix-principle-a-manifesto-for-programmers-in-the-ai-age-ca63317c5ebc"
	item, err := New().Fetch(ctx, postURL, core.DefaultOptions())
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("chrome not installed: %v", err)
		}
		t.Fatalf("Fetch via headless: %v", err)
	}
	if via, _ := item.Extra["via"].(string); via != "headless" {
		t.Errorf("Extra[via] = %q, want headless", via)
	}
	if strings.TrimSpace(item.Title) == "" {
		t.Errorf("missing title — og:title should always be present")
	}
	if len(item.Content) == 0 {
		t.Errorf("empty content — even Summary fallback failed")
	} else if len(item.Content) < 500 {
		t.Logf("warning: short content (%d chars) — Medium anti-bot may have served a degraded shell; full article needs the bridge", len(item.Content))
	}
	t.Logf("medium headless: title=%q content_chars=%d via=%v engine=%v",
		item.Title, len(item.Content), item.Extra["via"], item.Extra["engine"])
}
