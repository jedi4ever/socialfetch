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
