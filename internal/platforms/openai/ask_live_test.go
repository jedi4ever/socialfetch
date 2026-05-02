//go:build live

package openai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/dotenv"
)

// Live test — hits OpenAI's /v1/responses endpoint with the built-in
// web_search tool. Run with:
//
//	go test -tags=live -timeout 5m -run TestLiveOpenAIAsk ./internal/platforms/openai/...
//
// Requires OPENAI_API_KEY. Skipped silently if missing.
func TestLiveOpenAIAsk(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set — skipping")
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
	if answer.Provider != "openai" {
		t.Errorf("answer.Provider = %q, want openai", answer.Provider)
	}
	if answer.Model == "" {
		t.Errorf("answer.Model not set")
	}
	if answer.Asked.IsZero() {
		t.Errorf("answer.Asked not set")
	}
	if len(answer.Sources) == 0 {
		t.Logf("warning: no Sources returned — web_search may not have engaged for this query")
	}
	t.Logf("model=%s sources=%d text_chars=%d preview=%q",
		answer.Model, len(answer.Sources), len(answer.Text),
		preview(answer.Text, 200))
}

// TestLiveOpenAIAskWithInstructions exercises the system-prompt-style
// preamble plumbed through opts.Instructions → request.instructions.
func TestLiveOpenAIAskWithInstructions(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set — skipping")
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
