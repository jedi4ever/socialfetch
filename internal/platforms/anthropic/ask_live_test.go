//go:build live

package anthropic

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/util/dotenv"
)

// Live test — hits Anthropic's /v1/messages endpoint with the
// `web_search` server tool. Run with:
//
//	go test -tags=live -timeout 5m -run TestLiveAnthropicAsk ./internal/platforms/anthropic/...
//
// Requires ANTHROPIC_API_KEY. Skipped silently if missing.
//
// Anthropic's web search is enabled per organization in the Console;
// if your org admin hasn't toggled it on, the API returns a 4xx
// pointing to the privacy settings — surfaced via core.HTTPErrorBody
// at the top of the test failure.
func TestLiveAnthropicAsk(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set — skipping")
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
	if answer.Provider != "anthropic" {
		t.Errorf("answer.Provider = %q, want anthropic", answer.Provider)
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

// TestLiveAnthropicAskWithInstructions verifies the system prompt
// preamble (mapped from opts.Instructions to the top-level `system`
// field on the Messages API).
func TestLiveAnthropicAskWithInstructions(t *testing.T) {
	dotenv.LoadAuto()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set — skipping")
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
