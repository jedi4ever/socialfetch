package medium

import (
	"strings"
	"testing"

	"github.com/jedi4ever/socialfetch/internal/util/htmlmeta"
)

// Verifies the Medium extractor picks the host-specific .pw-post-body
// container instead of falling back to the generic <article> body. If
// Medium ever renames .pw-post-body again this test surfaces it
// immediately.
func TestMediumExtractorPicksHostSelector(t *testing.T) {
	const html = `<!DOCTYPE html>
<html><body>
  <section class="pw-post-body"><p>per-host body</p></section>
  <article><p>generic fallback</p></article>
</body></html>`

	page, err := htmlmeta.Parse(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, err := (&Extractor{}).Extract("https://medium.com/@a/x", page)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(got.Content, "per-host body") {
		t.Errorf("expected Medium selector hit, got: %q", got.Content)
	}
	if got.Source != "medium" {
		t.Errorf("source: %q, want medium", got.Source)
	}
}

func TestMediumExtractorMatch(t *testing.T) {
	ex := &Extractor{}
	cases := map[string]bool{
		"medium.com":             true,
		"alice.medium.com":       true,
		"engineering.medium.com": true,
		"example.com":            false,
		"medium.org":             false,
	}
	for host, want := range cases {
		if got := ex.Match(host); got != want {
			t.Errorf("Match(%q) = %v, want %v", host, got, want)
		}
	}
}
