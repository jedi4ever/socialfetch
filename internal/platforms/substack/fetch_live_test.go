//go:build live

package substack

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// TestLiveSubstackFetch hits a real Substack-hosted post. Like Medium,
// the fetcher is dual-path (bridge first, direct HTTP fallback) so the
// test works whether or not the bridge is running. We use Lenny's
// Newsletter — a well-established Substack publication that posts stay
// up.
//
// On a paywalled / member-only post we may only see the public excerpt;
// soft-warn on a short body rather than failing.
func TestLiveSubstackFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const postURL = "https://www.lennysnewsletter.com/"
	item, err := New().Fetch(ctx, postURL, core.DefaultOptions())
	if err != nil {
		t.Skipf("substack live fetch skipped (bridge + http both failed): %v", err)
	}
	if strings.TrimSpace(item.Title) == "" {
		t.Errorf("missing title")
	}
	if strings.TrimSpace(item.Content) == "" {
		t.Logf("warning: empty content — paywall or unrecognised layout")
	} else if len(item.Content) < 200 {
		t.Logf("warning: content is short (%d chars) — Substack may have served a paywall excerpt", len(item.Content))
	}
	t.Logf("title=%q content_chars=%d via=%v", item.Title, len(item.Content), item.Extra["via"])
}
