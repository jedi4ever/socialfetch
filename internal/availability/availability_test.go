package availability

import (
	"os"
	"strings"
	"testing"
)

func TestStatus(t *testing.T) {
	// Save + restore env around each subtest so we don't leak state.
	cases := []struct {
		name       string
		category   string
		provider   string
		setEnv     map[string]string
		clearEnvs  []string
		want       string // exact match for "" / "needs bridge", prefix for "missing"
		wantPrefix bool
	}{
		{
			name:      "no-auth provider always ok",
			category:  "search",
			provider:  "duckduckgo",
			clearEnvs: []string{},
			want:      "",
		},
		{
			name:      "missing single env",
			category:  "search",
			provider:  "brave",
			clearEnvs: []string{"BRAVE_API_KEY"},
			want:      "missing BRAVE_API_KEY",
		},
		{
			name:     "auth set",
			category: "search",
			provider: "brave",
			setEnv:   map[string]string{"BRAVE_API_KEY": "x"},
			want:     "",
		},
		{
			name:      "missing both env vars",
			category:  "search",
			provider:  "google",
			clearEnvs: []string{"GOOGLE_API_KEY", "GOOGLE_CSE_ID"},
			want:      "missing GOOGLE_API_KEY + GOOGLE_CSE_ID",
		},
		{
			name:      "missing one of two",
			category:  "search",
			provider:  "google",
			setEnv:    map[string]string{"GOOGLE_API_KEY": "x"},
			clearEnvs: []string{"GOOGLE_CSE_ID"},
			want:      "missing GOOGLE_CSE_ID",
		},
		{
			name:      "google ask: gemini key alone is enough",
			category:  "ask",
			provider:  "google",
			setEnv:    map[string]string{"GEMINI_API_KEY": "x"},
			clearEnvs: []string{"GOOGLE_API_KEY"},
			want:      "",
		},
		{
			name:      "google ask: google key alone is enough",
			category:  "ask",
			provider:  "google",
			setEnv:    map[string]string{"GOOGLE_API_KEY": "x"},
			clearEnvs: []string{"GEMINI_API_KEY"},
			want:      "",
		},
		{
			name:      "google ask: neither key set",
			category:  "ask",
			provider:  "google",
			clearEnvs: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
			want:      "missing GEMINI_API_KEY",
		},
		{
			name:     "bridge provider",
			category: "fetch",
			provider: "linkedin",
			want:     "needs bridge",
		},
		{
			name:     "unknown provider",
			category: "search",
			provider: "frobnicator",
			want:     "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Snapshot + restore env vars touched by this test.
			restore := map[string]string{}
			for k := range c.setEnv {
				restore[k] = osGet(t, k)
			}
			for _, k := range c.clearEnvs {
				restore[k] = osGet(t, k)
			}
			t.Cleanup(func() {
				for k, v := range restore {
					if v == "" {
						osUnset(t, k)
					} else {
						osSet(t, k, v)
					}
				}
			})

			for _, k := range c.clearEnvs {
				osUnset(t, k)
			}
			for k, v := range c.setEnv {
				osSet(t, k, v)
			}

			got := Status(c.category, c.provider)
			if c.wantPrefix {
				if !strings.HasPrefix(got, c.want) {
					t.Errorf("Status(%s,%s) = %q, want prefix %q", c.category, c.provider, got, c.want)
				}
				return
			}
			if got != c.want {
				t.Errorf("Status(%s,%s) = %q, want %q", c.category, c.provider, got, c.want)
			}
		})
	}
}

func TestAvailable(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "x")
	if !Available("search", "brave") {
		t.Error("brave should be Available with key set")
	}
	t.Setenv("BRAVE_API_KEY", "")
	if Available("search", "brave") {
		t.Error("brave should NOT be Available with key empty")
	}
	// Bridge providers report Available()==false because the bridge
	// is dynamic — caller should call social_fetch_bridge_status to
	// confirm liveness before invoking.
	if Available("fetch", "linkedin") {
		t.Error("linkedin fetch should report unavailable (bridge dynamic)")
	}
}

func osGet(t *testing.T, k string) string {
	t.Helper()
	return os.Getenv(k)
}
func osSet(t *testing.T, k, v string) {
	t.Helper()
	t.Setenv(k, v)
}
func osUnset(t *testing.T, k string) {
	t.Helper()
	t.Setenv(k, "")
}
