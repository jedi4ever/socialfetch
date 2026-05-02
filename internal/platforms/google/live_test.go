//go:build live

package google

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/dotenv"
)

// Live test — hits Google's Gemini API with the google_search
// grounding tool enabled. Run with:
//
//	go test -tags=live -timeout 5m ./internal/platforms/google/...
//
// Requires GEMINI_API_KEY (or GOOGLE_API_KEY) in the shell or in a
// .env at the repo root or next to the binary. Skipped silently if
// no key is available so CI without secrets stays green.
//
// The question is intentionally one whose answer Gemini must look up
// (recent Anthropic API news rather than evergreen facts) — that
// way an empty groundingMetadata.groundingChunks list is a real
// signal that grounding didn't engage.
func TestLiveGoogleAsk(t *testing.T) {
	// Best-effort: load .env files the same way the binary does so
	// running `go test -tags=live` without a pre-exported key still
	// works when the user has dropped credentials into the repo.
	for _, p := range []string{".env", "../../../.env"} {
		_ = dotenv.Load(p)
	}
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY / GOOGLE_API_KEY not set — skipping live google ask test")
	}

	// Gemini grounding loops can take 30-60s on multi-source queries.
	// The 3-minute ceiling matches longAskClient.Timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	answer, err := NewAsker().Ask(ctx, "What is the latest version of the Anthropic Claude API as of this week?", core.AskOptions{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.TrimSpace(answer.Text) == "" {
		t.Errorf("answer text is empty")
	}
	if answer.Provider != "google" {
		t.Errorf("answer.Provider = %q, want google", answer.Provider)
	}
	if answer.Model == "" {
		t.Errorf("answer.Model not set")
	}
	if answer.Asked.IsZero() {
		t.Errorf("answer.Asked not set")
	}
	// google_search engaging is the whole point of grounding; treat
	// missing sources as a soft warning rather than a hard fail
	// (grounding is the model's choice, not guaranteed for every
	// query).
	if len(answer.Sources) == 0 {
		t.Logf("warning: no Sources returned — grounding may not have engaged for this query")
	}

	t.Logf("model=%s sources=%d text_chars=%d preview=%q",
		answer.Model, len(answer.Sources), len(answer.Text),
		preview(answer.Text, 200))
}

// TestLiveGoogleAskWithInstructions exercises the new
// AskOptions.Instructions field end-to-end. We don't assert on the
// answer's adherence to the instruction (that's the model's job),
// just that the request succeeds — failing here would mean we built
// the systemInstruction field wrong.
func TestLiveGoogleAskWithInstructions(t *testing.T) {
	for _, p := range []string{".env", "../../../.env"} {
		_ = dotenv.Load(p)
	}
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY / GOOGLE_API_KEY not set — skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	answer, err := NewAsker().Ask(ctx, "Name three programming languages.", core.AskOptions{
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
