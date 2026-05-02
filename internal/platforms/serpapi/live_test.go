//go:build live

package serpapi

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/dotenv"
)

// Live test — hits SerpAPI's google_ai_overview engine. Run with:
//
//	go test -tags=live -timeout 5m -run TestLiveSerpAPIAsk ./internal/platforms/serpapi/...
//
// Requires SERPAPI_KEY. Skipped silently if missing.
//
// Note: not every query triggers an AI Overview from Google; the
// query below is one Google reliably produces an overview for. If
// SerpAPI returns "no AI Overview" the test logs it as a soft skip
// rather than failing — that's a Google decision, not a code bug.
func TestLiveSerpAPIAsk(t *testing.T) {
	for _, p := range []string{".env", "../../../.env"} {
		_ = dotenv.Load(p)
	}
	if os.Getenv("SERPAPI_KEY") == "" {
		t.Skip("SERPAPI_KEY not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	answer, err := NewAsker().Ask(ctx, "what is the capital of france", core.AskOptions{})
	if err != nil {
		// SerpAPI explicitly errors when no overview is generated for
		// the query — accept that as a non-failure for the live test.
		if strings.Contains(err.Error(), "no AI Overview") {
			t.Skipf("Google didn't generate an AI Overview for the test query: %v", err)
		}
		t.Fatalf("Ask: %v", err)
	}
	if strings.TrimSpace(answer.Text) == "" && len(answer.Sources) == 0 {
		t.Errorf("answer text and sources both empty")
	}
	if answer.Provider != "serpapi" {
		t.Errorf("answer.Provider = %q, want serpapi", answer.Provider)
	}
	if answer.Asked.IsZero() {
		t.Errorf("answer.Asked not set")
	}
	t.Logf("sources=%d text_chars=%d preview=%q",
		len(answer.Sources), len(answer.Text), preview(answer.Text, 200))
}

func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
