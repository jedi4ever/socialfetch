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
