//go:build live

package perplexity

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/util/dotenv"
)

// Live test — hits the real Perplexity Sonar API. Run with:
//
//	go test -tags=live -timeout 5m ./internal/platforms/perplexity/...
//
// Requires PERPLEXITY_API_KEY (or PPLX_API_KEY). Skipped silently if
// no key is available so CI without secrets stays green.
func TestLivePerplexityAsk(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("PERPLEXITY_API_KEY") == "" && os.Getenv("PPLX_API_KEY") == "" {
		t.Skip("PERPLEXITY_API_KEY / PPLX_API_KEY not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	answer, err := New().Ask(ctx, "What is the latest version of the Anthropic Claude API as of this week?", core.AskOptions{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.TrimSpace(answer.Text) == "" {
		t.Errorf("answer text is empty")
	}
	if answer.Provider != "perplexity" {
		t.Errorf("answer.Provider = %q, want perplexity", answer.Provider)
	}
	if answer.Model == "" {
		t.Errorf("answer.Model not set")
	}
	if answer.Asked.IsZero() {
		t.Errorf("answer.Asked not set")
	}
	if len(answer.Sources) == 0 {
		t.Logf("warning: no Sources returned — grounded search may not have engaged")
	}
	t.Logf("model=%s sources=%d text_chars=%d preview=%q",
		answer.Model, len(answer.Sources), len(answer.Text),
		preview(answer.Text, 200))
}

// TestLivePerplexityAskWithInstructions exercises the system-message
// preamble built from opts.Instructions.
func TestLivePerplexityAskWithInstructions(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("PERPLEXITY_API_KEY") == "" && os.Getenv("PPLX_API_KEY") == "" {
		t.Skip("PERPLEXITY_API_KEY / PPLX_API_KEY not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	answer, err := New().Ask(ctx, "Name three programming languages.", core.AskOptions{
		Instructions: "Always answer in bullet points starting with a hyphen.",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.TrimSpace(answer.Text) == "" {
		t.Errorf("answer text is empty")
	}
	t.Logf("preview=%q", preview(answer.Text, 200))
}

func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
