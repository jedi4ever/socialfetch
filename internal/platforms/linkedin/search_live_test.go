//go:build live

package linkedin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/bridge"
	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/util/dotenv"
)

// Live test — requires the local browser bridge to be running and
// the user to be logged into LinkedIn in the browser. Soft-skips
// when the bridge is unreachable so CI without the extension stays
// green.
//
//	go test -tags=live -timeout 5m -run TestLiveLinkedInSearch ./internal/platforms/linkedin/...
func TestLiveLinkedInSearch(t *testing.T) {
	dotenv.LoadAuto()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	results, err := NewSearchProvider().Search(ctx, "harness engineering", core.SearchOptions{Max: 10})
	if err != nil {
		if errors.Is(err, bridge.ErrBridgeUnreachable) || errors.Is(err, bridge.ErrNoExtensionAttached) {
			t.Skipf("bridge not available: %v", err)
		}
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Skip("0 results — possibly rate-limited or query yielded nothing; not a code bug")
	}
	first := results[0]
	if !strings.HasPrefix(first.URL, "https://www.linkedin.com/feed/update/urn:li:activity:") {
		t.Errorf("first result URL doesn't look like a LinkedIn activity URL: %q", first.URL)
	}
	if first.Source != "linkedin" {
		t.Errorf("source = %q, want linkedin", first.Source)
	}
	t.Logf("got %d results, first title=%q", len(results), first.Title)
}
