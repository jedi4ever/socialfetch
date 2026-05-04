//go:build live

package linkedin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/bridge"
	"github.com/jedi4ever/social-skills/internal/core"
)

// Live tests for LinkedIn require the local browser-extension bridge
// to be running with an authenticated session. When the bridge isn't
// reachable or no extension is attached, we soft-skip — those are
// environment conditions, not code regressions.
//
// Run with:
//
//	go test -tags=live -timeout 5m ./internal/platforms/linkedin/...

// TestLiveLinkedInFetch fetches Patrick Debois' profile — the owner of
// this repo, so the URL is as stable as it gets.
func TestLiveLinkedInFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	item, err := New().Fetch(ctx, "https://www.linkedin.com/in/patrickdebois/", core.DefaultOptions())
	if err != nil {
		if isBridgeEnvErr(err) {
			t.Skipf("bridge not available: %v", err)
		}
		t.Fatalf("Fetch: %v", err)
	}
	if strings.TrimSpace(item.Content) == "" {
		t.Errorf("missing content")
	}
	if strings.TrimSpace(item.Title) == "" {
		t.Logf("warning: empty title — LinkedIn may have rendered an unexpected layout")
	}
	t.Logf("title=%q author=%q content_chars=%d", item.Title, item.Author, len(item.Content))
}

// TestLiveLinkedInFetchMedia hits a known post (Cole Medin
// announcing Archon's 20k stars — the URL the user reported in the
// session that motivated this test) and verifies that media
// extraction produces at least one image. Stable enough as a fixture
// because public posts on LinkedIn don't get retroactively edited
// often, and even if Archon's 20k post drops we'd still see SOME
// media on a typical /posts/ URL — the assertion is "post media
// extracted, not zero" rather than "exactly this URL".
//
// Run with: go test -tags=live -run TestLiveLinkedInFetchMedia
//
//	./internal/platforms/linkedin/...
func TestLiveLinkedInFetchMedia(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const postURL = "https://www.linkedin.com/posts/cole-medin-727752184_archon-just-crossed-20000-github-stars-share-7454993154392502272-RSYe/"
	item, err := New().Fetch(ctx, postURL, core.DefaultOptions())
	if err != nil {
		if isBridgeEnvErr(err) {
			t.Skipf("bridge not available: %v", err)
		}
		t.Fatalf("Fetch: %v", err)
	}
	if len(item.Media) == 0 {
		t.Errorf("expected at least one media item, got none")
	}
	for i, m := range item.Media {
		// Every kept media must be on a LinkedIn-CDN host. If we
		// surface random external URLs, the chrome filter has
		// drifted and needs updating.
		if !strings.Contains(m.URL, "media.licdn.com") &&
			!strings.Contains(m.URL, "media-exp") {
			t.Errorf("media[%d] URL not on licdn CDN: %s", i, m.URL)
		}
		// Type should be one of the known shapes.
		switch m.Type {
		case "image", "video-poster":
		default:
			t.Errorf("media[%d] unexpected Type=%q", i, m.Type)
		}
	}
	if len(item.Media) > 0 {
		t.Logf("extracted %d media items (first: %s, type=%s)",
			len(item.Media), item.Media[0].URL, item.Media[0].Type)
	}
}

// TestLiveLinkedInFetchQuotedRepost confirms the rendered post body
// includes the embedded reposted/quoted post. LinkedIn nests the
// reshared post inside `feed-shared-mini-update` (which used to be
// in cleanHTML's deny-list and silently stripped). After the deny
// fix the embedded card stays in the DOM and htmlmd.Convert renders
// its text inline.
//
// Fixture URL is a known reshare on LinkedIn; if it goes away,
// swap with any post that has a quoted/reshared inner post. The
// assertion is intentionally soft — we look for SOME signal of an
// embedded post (a second author / a second timestamp / a "reposted"
// affordance) rather than a specific phrase, so the test survives
// LinkedIn DOM rotations.
func TestLiveLinkedInFetchQuotedRepost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Hugo de Gooijer's repost of an AI Engineer Europe talk
	// — confirmed by the maintainer to embed an original post
	// inline. If it goes away, swap with any post whose URL ends
	// in `-activity-...` and has visible reshare commentary.
	const repostURL = "https://www.linkedin.com/posts/hugodegooijer_one-of-my-favorite-talks-at-ai-engineer-europe-activity-7454456510899945473-RJ-L"
	item, err := New().Fetch(ctx, repostURL, core.DefaultOptions())
	if err != nil {
		if isBridgeEnvErr(err) {
			t.Skipf("bridge not available: %v", err)
		}
		t.Fatalf("Fetch: %v", err)
	}

	// The rendered body should be substantially longer than a
	// single-post fetch because the embedded reshare contributes
	// its own text. Single LinkedIn posts typically run 500-2000
	// chars; reshares should clear ~1500.
	if len(item.Content) < 1500 {
		t.Errorf("body too short for a reshare (%d chars) — embedded post may have been stripped", len(item.Content))
	}

	// The fixture URL is Hugo de Gooijer resharing a Patrick
	// Debois post about AI agents and CDLC. Assert a phrase
	// that's specifically from the EMBEDDED original (not Hugo's
	// commentary) — if it disappears the embed is being stripped
	// again. Update this assertion when swapping the fixture URL.
	const embeddedPostMarker = "AI agents start from zero every session"
	if !strings.Contains(item.Content, embeddedPostMarker) {
		t.Errorf("expected embedded-post phrase %q not found — `feed-shared-mini-update` deny-list may have re-emerged. content head:\n%.500s",
			embeddedPostMarker, item.Content)
	}
	t.Logf("linkedin reshare body=%d chars, embedded marker found", len(item.Content))
}

// isBridgeEnvErr reports whether err looks like a missing-bridge or
// missing-extension condition — both of which are environment problems
// rather than code regressions, so we should skip rather than fail.
func isBridgeEnvErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, bridge.ErrBridgeUnreachable) || errors.Is(err, bridge.ErrNoExtensionAttached) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "bridge daemon not running") ||
		strings.Contains(msg, "no extension attached") ||
		strings.Contains(msg, "bridge: ")
}
