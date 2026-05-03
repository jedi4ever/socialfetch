//go:build live

package tavily

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Live test — hits Tavily's /search endpoint with include_answer=true.
// Run with:
//
//	go test -tags=live -timeout 5m -run TestLiveTavilyAsk ./internal/platforms/tavily/...
//
// Requires TAVILY_API_KEY. Skipped silently if missing.
func TestLiveTavilyAsk(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("TAVILY_API_KEY") == "" {
		t.Skip("TAVILY_API_KEY not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	answer, err := NewAsker().Ask(ctx, "What is the latest version of the Anthropic Claude API as of this week?", core.AskOptions{Recency: "week"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.TrimSpace(answer.Text) == "" {
		t.Errorf("answer text is empty")
	}
	if answer.Provider != "tavily" {
		t.Errorf("answer.Provider = %q, want tavily", answer.Provider)
	}
	if answer.Asked.IsZero() {
		t.Errorf("answer.Asked not set")
	}
	if len(answer.Sources) == 0 {
		t.Errorf("expected at least one Source from Tavily search")
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
