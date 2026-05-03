package ledger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// fakeBinary writes a tiny shell script that records its stdin to
// the supplied capture file and exits 0. Lets us test Ingest without
// depending on an actual socialfetch-ledger build.
func fakeBinary(t *testing.T, capturePath string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "socialfetch-ledger")
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
	bin := filepath.Join(dir, "socialfetch-ledger")
	script := "#!/bin/sh\necho 'simulated ledger failure' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake failing binary: %v", err)
	}
	return bin
}

func TestEnabledTruthyForms(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"no":    false,
		"1":     true,
		"true":  true,
		"yes":   true,
		"on":    true,
		"  1  ": true,  // trim whitespace
		"True":  true,  // case-insensitive
		"YES":   true,
	}
	for v, want := range cases {
		t.Setenv(EnabledEnv, v)
		if got := Enabled(); got != want {
			t.Errorf("Enabled() with %q: got %v, want %v", v, got, want)
		}
	}
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

	t.Setenv(EnabledEnv, "")
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
