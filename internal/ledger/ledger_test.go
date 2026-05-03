package ledger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
)

// fakeBinary writes a tiny shell script that records its stdin to
// the supplied capture file and exits 0. Lets us test Ingest without
// depending on an actual social-ledger build.
func fakeBinary(t *testing.T, capturePath string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "social-ledger")
	script := "#!/bin/sh\ncat > " + capturePath + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return bin
}

// failingBinary exits non-zero with a message on stderr — used to
// confirm Ingest swallows the error rather than propagating.
func failingBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "social-ledger")
	script := "#!/bin/sh\necho 'simulated ledger failure' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake failing binary: %v", err)
	}
	return bin
}

func TestEnabledExplicitForms(t *testing.T) {
	// Explicit values bypass auto-detection entirely. Pin the env
	// var to a non-empty BIN path that doesn't resolve so the
	// auto-detect fallback (if accidentally taken) would fail —
	// a regression that broke the explicit semantics would show
	// up as "1 → false" instead of silently defaulting.
	t.Setenv(BinaryEnv, "/nonexistent/social-ledger")

	cases := map[string]bool{
		"1":     true,
		"true":  true,
		"yes":   true,
		"on":    true,
		"  1  ": true, // trim whitespace
		"True":  true, // case-insensitive
		"YES":   true,
		"0":     false,
		"false": false,
		"no":    false,
		"off":   false,
	}
	for v, want := range cases {
		resetAutoDetectForTests()
		t.Setenv(EnabledEnv, v)
		if got := Enabled(); got != want {
			t.Errorf("Enabled() with %q: got %v, want %v", v, got, want)
		}
	}
}

func TestEnabledAutoDetect(t *testing.T) {
	t.Run("binary present → auto enables", func(t *testing.T) {
		resetAutoDetectForTests()
		// Use the fake binary as the BIN target so binaryPath()
		// resolves successfully without depending on $PATH.
		bin := fakeBinary(t, filepath.Join(t.TempDir(), "ignored.jsonl"))
		t.Setenv(BinaryEnv, bin)
		t.Setenv(EnabledEnv, "")
		if !Enabled() {
			t.Error("Enabled() with empty env + binary present: got false, want true")
		}
	})

	t.Run("binary missing → auto stays off", func(t *testing.T) {
		resetAutoDetectForTests()
		// Empty BIN, $PATH won't have social-ledger in a
		// fresh test process either (we're not running an
		// integration build).
		t.Setenv(BinaryEnv, "")
		t.Setenv(EnabledEnv, "")
		t.Setenv("PATH", "/nonexistent")
		if Enabled() {
			t.Error("Enabled() with empty env + no binary: got true, want false")
		}
	})

	t.Run("auto literal works the same as empty", func(t *testing.T) {
		resetAutoDetectForTests()
		t.Setenv(BinaryEnv, "")
		t.Setenv(EnabledEnv, "auto")
		t.Setenv("PATH", "/nonexistent")
		if Enabled() {
			t.Error("Enabled() with auto + no binary: got true, want false")
		}
	})

	t.Run("explicit off beats present binary", func(t *testing.T) {
		resetAutoDetectForTests()
		bin := fakeBinary(t, filepath.Join(t.TempDir(), "ignored.jsonl"))
		t.Setenv(BinaryEnv, bin)
		t.Setenv(EnabledEnv, "0")
		if Enabled() {
			t.Error("Enabled() with =0 + binary present: got true, want false")
		}
	})
}

func TestIngestPipesJSONLToBinary(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "captured.jsonl")
	bin := fakeBinary(t, capture)

	t.Setenv(EnabledEnv, "1")
	t.Setenv(BinaryEnv, bin)

	items := []core.Item{
		{Source: "hackernews", URL: "https://news.ycombinator.com/item?id=1", Title: "first"},
		{Source: "github", URL: "https://github.com/golang/go", Title: "the source"},
	}
	Ingest(context.Background(), items...)

	body, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), string(body))
	}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d not valid JSON: %v\n%s", i, err, line)
		}
		if got["url"] != items[i].URL {
			t.Errorf("line %d url=%v, want %v", i, got["url"], items[i].URL)
		}
	}
}

func TestIngestNoOpWhenDisabled(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "captured.jsonl")
	bin := fakeBinary(t, capture)

	// SOCIALFETCH_LEDGER=0 is explicit-off and beats auto-detect
	// even when the binary path resolves successfully — the
	// no-op-when-disabled guarantee.
	resetAutoDetectForTests()
	t.Setenv(EnabledEnv, "0")
	t.Setenv(BinaryEnv, bin)

	Ingest(context.Background(), core.Item{URL: "x"})
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Errorf("capture file exists despite disabled ingest: %v", err)
	}
}

func TestIngestSwallowsBinaryFailure(t *testing.T) {
	bin := failingBinary(t)
	t.Setenv(EnabledEnv, "1")
	t.Setenv(BinaryEnv, bin)

	// Ingest is `func ... ()` — no error to check. Just confirm it
	// doesn't panic / deadlock and returns. Failure goes through
	// the audit logger which we don't attach here.
	Ingest(context.Background(), core.Item{URL: "x"})
}

func TestIngestNoOpOnEmptyItems(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "captured.jsonl")
	bin := fakeBinary(t, capture)
	t.Setenv(EnabledEnv, "1")
	t.Setenv(BinaryEnv, bin)

	Ingest(context.Background()) // no items
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Errorf("capture file exists despite empty ingest: %v", err)
	}
}
