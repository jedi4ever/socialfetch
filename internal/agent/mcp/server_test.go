package mcp

// server_test.go covers the per-run-session output directory used
// by social_agent_run to stash artifacts before container teardown.
// Without this the streaming-mode one-shot path would emit artifact
// metadata events that point at files no longer reachable by the
// time the response lands client-side.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/agent/streaming"
)

func TestNewSessionDir_CreatesArtifactsSubdirAndIsUnique(t *testing.T) {
	rootA, artA, err := newSessionDir()
	if err != nil {
		t.Fatalf("first newSessionDir: %v", err)
	}
	defer os.RemoveAll(rootA)

	if _, err := os.Stat(rootA); err != nil {
		t.Errorf("session root %q does not exist: %v", rootA, err)
	}
	if _, err := os.Stat(artA); err != nil {
		t.Errorf("artifacts dir %q does not exist: %v", artA, err)
	}
	if filepath.Base(artA) != "artifacts" {
		t.Errorf("artifacts dir base = %q, want \"artifacts\"", filepath.Base(artA))
	}
	if filepath.Dir(artA) != rootA {
		t.Errorf("artifacts dir parent = %q, want %q", filepath.Dir(artA), rootA)
	}

	want := filepath.Join(os.TempDir(), "social-agent")
	if !strings.HasPrefix(rootA, want+string(os.PathSeparator)) {
		t.Errorf("session root %q does not live under %q", rootA, want)
	}

	rootB, _, err := newSessionDir()
	if err != nil {
		t.Fatalf("second newSessionDir: %v", err)
	}
	defer os.RemoveAll(rootB)

	if rootA == rootB {
		t.Errorf("two consecutive calls collided on %q — should have a unique random suffix", rootA)
	}
}

func TestProgressSummary_HumanReadable(t *testing.T) {
	cases := []struct {
		name string
		e    streaming.Event
		want string
	}{
		{"text trims whitespace", streaming.Event{Kind: "text", Content: "  hello world  "}, "hello world"},
		{"text empty → skip", streaming.Event{Kind: "text", Content: "   "}, ""},
		{"artifact with size + mime", streaming.Event{Kind: "artifact", Path: "test.md", Size: 158, Mime: "text/markdown"}, "wrote test.md (158 bytes, text/markdown)"},
		{"session up shortens id", streaming.Event{Kind: "session", Status: "up", ID: "5023c54a4c9a35f43c0916170a83bf2780d2dfe54ad8f333c400ca510c5f4e7c"}, "session up: 5023c54a4c9a"},
		{"session down", streaming.Event{Kind: "session", Status: "down", ID: "abc123"}, "session down: abc123"},
		{"done ok", streaming.Event{Kind: "done", ExitCode: 0}, "done (exit 0)"},
		{"done with error", streaming.Event{Kind: "done", ExitCode: 1, Error: "boom"}, "done with error: boom"},
		{"error event", streaming.Event{Kind: "error", Error: "thing failed"}, "error: thing failed"},
		{"claude_event noise → skip", streaming.Event{Kind: "claude_event"}, ""},
		{"unknown kind → skip", streaming.Event{Kind: "future-kind"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := progressSummary(c.e); got != c.want {
				t.Errorf("progressSummary(%+v) = %q, want %q", c.e, got, c.want)
			}
		})
	}
}

func TestProgressSummary_TextTruncates(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := progressSummary(streaming.Event{Kind: "text", Content: long})
	if len(got) != 160 {
		t.Errorf("long text length = %d, want 160 (157 + \"...\")", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("long text not truncated with ellipsis: %q", got)
	}
}

func TestNewSessionDir_NameShape(t *testing.T) {
	root, _, err := newSessionDir()
	if err != nil {
		t.Fatalf("newSessionDir: %v", err)
	}
	defer os.RemoveAll(root)

	base := filepath.Base(root)
	// Format: 20060102T150405-<8 hex> = 15 + 1 + 8 = 24 chars.
	if len(base) != 24 {
		t.Errorf("base name %q length = %d, want 24 (timestamp-hex)", base, len(base))
	}
	if base[15] != '-' {
		t.Errorf("base name %q missing '-' separator at index 15", base)
	}
}
