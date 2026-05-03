package arxiv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
)

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://arxiv.org/abs/2403.04132", true},
		{"https://arxiv.org/abs/2403.04132v3", true},
		{"https://arxiv.org/pdf/2403.04132", true},
		{"https://arxiv.org/pdf/2403.04132.pdf", true},
		{"https://arxiv.org/html/2403.04132", true},
		{"https://arxiv.org/", false},
		{"https://example.com/abs/2403.04132", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestExtractID(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://arxiv.org/abs/2403.04132", "2403.04132"},
		{"https://arxiv.org/abs/2403.04132v2", "2403.04132v2"},
		{"https://arxiv.org/pdf/2403.04132.pdf", "2403.04132"},
		{"https://arxiv.org/abs/cs.LG/9301001", "cs.LG/9301001"},
	}
	for _, c := range cases {
		got, err := extractID(c.raw)
		if err != nil || got != c.want {
			t.Errorf("extractID(%q) = (%q, %v); want %q", c.raw, got, err, c.want)
		}
	}
}

const fakeAtom = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>http://arxiv.org/abs/2403.04132v1</id>
    <title>Some Paper Title</title>
    <summary>
      Multiline
      abstract.
    </summary>
    <published>2026-03-15T10:00:00Z</published>
    <author><name>Alice Smith</name></author>
    <author><name>Bob Jones</name></author>
    <category term="cs.LG"/>
    <category term="cs.AI"/>
    <link rel="alternate" type="text/html" href="https://arxiv.org/abs/2403.04132v1"/>
    <link rel="related" title="pdf" href="https://arxiv.org/pdf/2403.04132v1"/>
  </entry>
</feed>`

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id_list") != "2403.04132" {
			t.Errorf("id_list = %q", r.URL.Query().Get("id_list"))
		}
		fmt.Fprint(w, fakeAtom)
	}))
	defer srv.Close()
	f := New()
	f.BaseURL = srv.URL
	// Disable body enrichment so the test stays hermetic — otherwise
	// Fetch would probe real arxiv.org/html/ + r.jina.ai. The
	// enrichment path has its own coverage in fetch_live_test.go.
	f.EnrichBody = false

	item, err := f.Fetch(context.Background(),
		"https://arxiv.org/abs/2403.04132",
		core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Source != "arxiv" || item.Kind != "paper" {
		t.Errorf("source/kind: %s/%s", item.Source, item.Kind)
	}
	if item.Title != "Some Paper Title" {
		t.Errorf("title = %q", item.Title)
	}
	if item.Author != "Alice Smith, Bob Jones" {
		t.Errorf("authors = %q", item.Author)
	}
	if !strings.Contains(item.Content, "Multiline abstract") {
		t.Errorf("body whitespace not collapsed: %q", item.Content)
	}
	if len(item.Tags) != 2 || item.Tags[0] != "cs.LG" {
		t.Errorf("tags = %v", item.Tags)
	}
	if item.Extra["pdf_url"] != "https://arxiv.org/pdf/2403.04132v1" {
		t.Errorf("pdf url not extracted: %v", item.Extra["pdf_url"])
	}
}
