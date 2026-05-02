package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

func sampleItem() *core.Item {
	pub := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	return &core.Item{
		Source:    "hackernews",
		Kind:      "story",
		URL:       "https://news.ycombinator.com/item?id=1",
		Title:     "Hello",
		Author:    "alice",
		AuthorURL: "https://news.ycombinator.com/user?id=alice",
		Published: &pub,
		Score:     42,
		Summary:   "A summary line.",
		Content:   "Body **markdown** here.",
		Tags:      []string{"tech", "news"},
		Media:     []core.Media{{URL: "https://example.com/x.png", Type: "image"}},
		Comments: []core.Comment{
			{ID: "c1", Author: "bob", Body: "First!", Depth: 0, Published: &pub},
			{ID: "c2", Author: "carol", Body: "Reply", Depth: 1, Published: &pub},
		},
		FetchedAt: pub,
	}
}

func TestParseFormat(t *testing.T) {
	cases := map[string]Format{
		"json":     FormatJSON,
		"JSONL":    FormatJSONL,
		"ndjson":   FormatJSONL,
		"markdown": FormatMarkdown,
		"md":       FormatMarkdown,
	}
	for in, want := range cases {
		got, err := ParseFormat(in)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Errorf("expected error for unknown format")
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := Item(&buf, sampleItem(), FormatJSON); err != nil {
		t.Fatal(err)
	}
	var got Envelope
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if got.WrittenAt.IsZero() {
		t.Errorf("written_at not set")
	}
	if got.Item.Title != "Hello" {
		t.Errorf("round-trip lost title")
	}
}

func TestRenderJSONL(t *testing.T) {
	var buf bytes.Buffer
	if err := Item(&buf, sampleItem(), FormatJSONL); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Errorf("jsonl should be single-line, got: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("jsonl must end with newline")
	}
}

func TestRenderMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := Item(&buf, sampleItem(), FormatMarkdown); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"# Hello",
		"**Source:** hackernews / story",
		"**Author:** alice",
		"**Score:** 42",
		"**Tags:** tech, news",
		"**Fetched:**",
		"**Written:**",
		"Body **markdown** here.",
		"## Media",
		"![image](https://example.com/x.png)",
		"## Comments",
		"- **bob**",
		"  - **carol**",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown missing %q\n--- output ---\n%s", want, got)
		}
	}
}
