package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
)

func TestWriteContentTemp(t *testing.T) {
	body := "# Title\n\nbody text"
	path, n, err := writeContentTemp("test", "md", body)
	if err != nil {
		t.Fatalf("writeContentTemp: %v", err)
	}
	defer os.Remove(path)
	if n != len(body) {
		t.Errorf("byte count = %d, want %d", n, len(body))
	}
	if !strings.HasPrefix(filepath.Base(path), "social-fetch-test-") {
		t.Errorf("filename doesn't carry the tool prefix: %s", path)
	}
	if filepath.Ext(path) != ".md" {
		t.Errorf("ext = %s, want .md", filepath.Ext(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != body {
		t.Errorf("body mismatch:\nwant %q\ngot  %q", body, string(got))
	}
}

func TestSafeTempPath(t *testing.T) {
	// Valid: a real temp file we just wrote.
	path, _, err := writeContentTemp("safe", "md", "ok")
	if err != nil {
		t.Fatalf("writeContentTemp: %v", err)
	}
	defer os.Remove(path)

	if _, err := safeTempPath(path); err != nil {
		t.Errorf("legitimate temp path rejected: %v", err)
	}

	// Reject: outside TempDir.
	if _, err := safeTempPath("/etc/passwd"); err == nil {
		t.Error("expected /etc/passwd to be rejected")
	}

	// Reject: under TempDir but not our prefix.
	other, err := os.CreateTemp("", "other-*.md")
	if err != nil {
		t.Fatalf("create other temp: %v", err)
	}
	other.Close()
	defer os.Remove(other.Name())
	if _, err := safeTempPath(other.Name()); err == nil {
		t.Errorf("expected non-social-fetch- prefix to be rejected: %s", other.Name())
	}

	// Reject: relative path.
	if _, err := safeTempPath("social-fetch-foo.md"); err == nil {
		t.Error("expected relative path to be rejected")
	}
}

func TestClassifyProvenance(t *testing.T) {
	cases := []struct{ src, want string }{
		{"hackernews", "auto-fetched"},
		{"HackerNews", "auto-fetched"},
		{"reddit", "auto-fetched"},
		{"x", "auto-fetched"},
		{"twitter", "auto-fetched"},
		{"linkedin", "auto-fetched"},
		{"article", "auto-fetched"},
		{"webfetch", "agent-recorded"},
		{"manual", "agent-recorded"},
		{"research-tool", "agent-recorded"},
		{"", "unknown"},
		{"frobnicator", "unknown"},
	}
	for _, c := range cases {
		if got := classifyProvenance(c.src); got != c.want {
			t.Errorf("classifyProvenance(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestThinContentHint(t *testing.T) {
	// Real article — should not fire.
	full := strings.Repeat("This is a paragraph of real article prose with substance and detail. ", 50)
	if h := thinContentHint(&core.Item{Source: "article", Title: "Real article", Content: full}); h != "" {
		t.Errorf("real article shouldn't trigger hint, got: %s", h)
	}
	// Tiny body — should fire.
	if h := thinContentHint(&core.Item{Source: "article", Title: "Stub", Content: "short"}); h == "" {
		t.Error("tiny body should trigger hint")
	}
	// Nav-heavy body (Stripe.dev shape): substantial bytes but mostly links.
	navHeavy := strings.Repeat("[Blog](/blog) [Pricing](/pricing) [Docs](https://docs.example.com) ", 80)
	if h := thinContentHint(&core.Item{Source: "article", Title: "Stripe-like", Content: navHeavy}); h == "" {
		t.Errorf("nav-heavy body should trigger hint, content len=%d prose=%d", len(navHeavy), countProse(navHeavy))
	}
	// Platform source (hackernews) — legitimately small, never hint.
	if h := thinContentHint(&core.Item{Source: "hackernews", Title: "x", Content: ""}); h != "" {
		t.Errorf("hackernews shouldn't trigger hint, got: %s", h)
	}
	// Empty title — bail (probably a fetch failure, not thin extraction).
	if h := thinContentHint(&core.Item{Source: "article", Title: "", Content: "short"}); h != "" {
		t.Errorf("missing title shouldn't trigger hint, got: %s", h)
	}
}

func TestParseLedgerFrontmatter(t *testing.T) {
	md := `# Some Title

**Source:** hackernews
**Author:** dang
**URL:** https://news.ycombinator.com/item?id=1
**Score:** 42

body content here that should not be parsed
**Source:** ignored
`
	got := parseLedgerFrontmatter(md)
	if got["source"] != "hackernews" {
		t.Errorf("source = %q, want hackernews", got["source"])
	}
	if got["author"] != "dang" {
		t.Errorf("author = %q, want dang", got["author"])
	}
	if got["score"] != "42" {
		t.Errorf("score = %q, want 42", got["score"])
	}
}
