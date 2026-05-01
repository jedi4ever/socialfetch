package main

import (
	"strings"
	"testing"
)

// CLI flag parsing is the part most likely to drift; the live network
// behavior is covered by per-source tests.

func TestParseFetchFlags(t *testing.T) {
	args := []string{
		"https://news.ycombinator.com/item?id=1",
		"-f", "json",
		"-o", "out/",
		"--no-comments",
		"--max-comments", "50",
		"--timeout", "10s",
		"-l", "audit.log",
		"https://github.com/foo/bar",
	}
	f, err := parseFetchFlags(args)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.format != "json" {
		t.Errorf("format: %q", f.format)
	}
	if f.output != "out/" {
		t.Errorf("output: %q", f.output)
	}
	if f.comments {
		t.Errorf("--no-comments not honored")
	}
	if f.maxComment != 50 {
		t.Errorf("max comments: %d", f.maxComment)
	}
	if f.timeout.Seconds() != 10 {
		t.Errorf("timeout: %v", f.timeout)
	}
	if f.logFile != "audit.log" {
		t.Errorf("log: %q", f.logFile)
	}
	if len(f.urls) != 2 {
		t.Errorf("urls: %v", f.urls)
	}
}

func TestParseFetchFlagsRejectsUnknown(t *testing.T) {
	if _, err := parseFetchFlags([]string{"--foo"}); err == nil {
		t.Errorf("expected error for unknown flag")
	}
}

func TestParseSearchFlags(t *testing.T) {
	args := []string{"-p", "serpapi", "-n", "20", "claude", "code"}
	f, err := parseSearchFlags(args)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.provider != "serpapi" {
		t.Errorf("provider: %q", f.provider)
	}
	if f.max != 20 {
		t.Errorf("max: %d", f.max)
	}
	if f.query != "claude code" {
		t.Errorf("query: %q", f.query)
	}
}

func TestSafeFilename(t *testing.T) {
	got := safeFilename("https://news.ycombinator.com/item?id=42")
	if strings.ContainsAny(got, "/?&=:") {
		t.Errorf("unsafe chars in %q", got)
	}
}

func TestIsDirOutput(t *testing.T) {
	if !isDirOutput("foo/") {
		t.Error("trailing slash should be dir")
	}
	if isDirOutput("file.json") {
		t.Error("plain name should not be dir")
	}
	if isDirOutput("") || isDirOutput("-") {
		t.Error("empty/'-' should not be dir")
	}
}

func TestExampleForKnownNames(t *testing.T) {
	for _, name := range []string{"hackernews", "reddit", "github", "twitter", "rss", "article"} {
		if exampleFor(name) == "" {
			t.Errorf("missing example for %s", name)
		}
	}
}
