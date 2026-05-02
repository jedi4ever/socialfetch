package main

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/render"
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
		"-j", "8",
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

// fakeFetcher is a stand-in for any source. It claims every URL, sleeps
// the configured delay, then returns an Item whose title encodes the URL
// so tests can verify ordering.
type fakeFetcher struct {
	delays map[string]time.Duration // per-URL artificial delay
	calls  atomic.Int64
}

func (*fakeFetcher) Name() string          { return "fake" }
func (*fakeFetcher) Match(u *url.URL) bool { return true }

func (f *fakeFetcher) Fetch(ctx context.Context, raw string, _ core.Options) (*core.Item, error) {
	f.calls.Add(1)
	if d, ok := f.delays[raw]; ok {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &core.Item{
		Source:    "fake",
		Kind:      "test",
		URL:       raw,
		Title:     "T:" + raw,
		FetchedAt: time.Now().UTC(),
	}, nil
}

// TestFetchStreamOrderedKeepsInputOrder makes the *first* URL slow and the
// rest fast. With jobs=4 they all run concurrently, but output order must
// match input order — the fast ones wait on the slow one.
func TestFetchStreamOrderedKeepsInputOrder(t *testing.T) {
	urls := []string{"u1", "u2", "u3", "u4"}
	f := &fakeFetcher{delays: map[string]time.Duration{
		"u1": 50 * time.Millisecond,
	}}
	reg := core.NewRegistry(f)

	var buf bytes.Buffer
	if err := fetchStreamOrdered(context.Background(), reg, urls, core.DefaultOptions(), render.FormatJSONL, &buf, 4); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	out := buf.String()
	idx := func(needle string) int { return strings.Index(out, needle) }
	for i := 0; i < len(urls)-1; i++ {
		a, b := idx("T:"+urls[i]), idx("T:"+urls[i+1])
		if a < 0 || b < 0 {
			t.Fatalf("missing url in output: %s", out)
		}
		if a > b {
			t.Errorf("url %s rendered after %s; want input order", urls[i], urls[i+1])
		}
	}
	if got := f.calls.Load(); got != int64(len(urls)) {
		t.Errorf("expected %d calls, got %d", len(urls), got)
	}
}

// TestFetchStreamOrderedRunsInParallel verifies jobs > 1 actually
// parallelizes. Four URLs each with a 40ms delay and jobs=4 should finish
// in ~40ms wall-clock; sequential would be ~160ms.
func TestFetchStreamOrderedRunsInParallel(t *testing.T) {
	urls := []string{"a", "b", "c", "d"}
	delay := 40 * time.Millisecond
	delays := map[string]time.Duration{}
	for _, u := range urls {
		delays[u] = delay
	}
	reg := core.NewRegistry(&fakeFetcher{delays: delays})

	var buf bytes.Buffer
	start := time.Now()
	if err := fetchStreamOrdered(context.Background(), reg, urls, core.DefaultOptions(), render.FormatJSONL, &buf, 4); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	elapsed := time.Since(start)
	// Allow generous slack for slow CI: anything under 3 × delay proves
	// it's running in parallel rather than sequential 4 × delay.
	if elapsed > 3*delay {
		t.Errorf("expected parallel run < 3*delay (%v); took %v", 3*delay, elapsed)
	}
	if n := strings.Count(buf.String(), `"source":`); n != len(urls) {
		t.Errorf("expected %d items in output, got %d", len(urls), n)
	}
}
