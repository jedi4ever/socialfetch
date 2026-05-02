//go:build live

package grok

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/util/dotenv"
)

// Live test — hits xAI's real Grok API with Live Search enabled.
// Run with:
//
//	go test -tags=live ./internal/platforms/grok/...
//
// Requires XAI_API_KEY (or GROK_API_KEY) in the shell or in a .env at
// the repo root or next to the binary. Skipped silently if no key is
// available so CI without secrets stays green.
//
// The question is intentionally one whose answer Grok must look up
// (recent OpenAI/Microsoft news rather than evergreen facts) — that
// way an empty/missing source list would be a real signal that Live
// Search isn't engaging, not just "Grok knew the answer cold."
func TestLiveGrokAsk(t *testing.T) {
	// Best-effort: load .env files the same way the binary does, so
	// running `go test -tags=live` without a pre-exported key still
	// works when the user has dropped credentials into the repo.
	dotenv.LoadAuto()
	if os.Getenv("XAI_API_KEY") == "" && os.Getenv("GROK_API_KEY") == "" {
		t.Skip("XAI_API_KEY / GROK_API_KEY not set — skipping live grok ask test")
	}

	// xAI's Agent Tools loop (web_search → browse → reason) regularly
	// runs 30-90s on questions that require multiple sources. Allow up
	// to 3 minutes; the underlying HTTP client is configured with a
	// matching timeout in ask.go.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	answer, err := New().Ask(ctx, "What is the latest version of the Claude API as of this week?", core.AskOptions{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.TrimSpace(answer.Text) == "" {
		t.Errorf("answer text is empty")
	}
	if answer.Provider != "grok" {
		t.Errorf("answer.Provider = %q, want grok", answer.Provider)
	}
	if answer.Model == "" {
		t.Errorf("answer.Model not set")
	}
	if answer.Asked.IsZero() {
		t.Errorf("answer.Asked not set")
	}
	// Live Search is enabled per-request; a question that requires
	// looking up recent news should produce at least one source.
	// Treat missing sources as a soft warning rather than a hard fail
	// (xAI's grounding behaviour can vary on individual queries).
	if len(answer.Sources) == 0 {
		t.Logf("warning: no Sources returned — Live Search may not have engaged for this query")
	}

	t.Logf("model=%s sources=%d text_chars=%d preview=%q",
		answer.Model, len(answer.Sources), len(answer.Text),
		preview(answer.Text, 200))
}

func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
