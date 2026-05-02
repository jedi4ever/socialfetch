//go:build live

package google

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/util/dotenv"
)

// TestLiveGoogleSearch hits the Custom Search JSON API. Requires both
// GOOGLE_API_KEY and GOOGLE_CSE_ID; skipped otherwise. Confirms the
// search.Provider wiring (separate from Gemini grounding above).
func TestLiveGoogleSearch(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("GOOGLE_API_KEY") == "" || os.Getenv("GOOGLE_CSE_ID") == "" {
		t.Skip("GOOGLE_API_KEY / GOOGLE_CSE_ID not set — skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results, err := New().Search(ctx, "anthropic claude api", core.SearchOptions{Max: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected at least one result, got 0")
	}
	t.Logf("got %d results", len(results))
}
