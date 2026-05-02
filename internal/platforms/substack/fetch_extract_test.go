package substack

import (
	"strings"
	"testing"

	"github.com/jedi4ever/socialfetch/internal/util/htmlmeta"
)

// substackPage exercises the Substack-specific selectors: .body.markup
// for the article body, h3.subtitle-text for the subtitle. Nav/footer
// cruft must NOT leak into the markdown output.
const substackPage = `<!DOCTYPE html>
<html><head>
  <title>How to ship</title>
  <meta property="og:title" content="How to ship">
  <meta property="og:description" content="A short essay">
  <meta property="og:site_name" content="Patrick's Substack">
  <link rel="canonical" href="https://patrick.substack.com/p/how-to-ship">
  <script type="application/ld+json">
  {"@type":"Article","author":{"name":"Patrick"},"datePublished":"2026-01-15T08:00:00Z"}
  </script>
</head><body>
  <nav>SUBSTACK NAV — should not appear</nav>
  <div class="topbar">SIGN UP — should not appear</div>
  <article>
    <h1>How to ship</h1>
    <h3 class="subtitle-text">A short essay on shipping software</h3>
    <div class="body markup">
      <p>The first step is shipping.</p>
      <p>The second step is also shipping.</p>
    </div>
  </article>
  <span class="like-count">142</span>
  <button class="post-ufi-comment-button">37</button>
  <footer>FOOTER — should not appear</footer>
</body></html>`

func TestSubstackExtractor(t *testing.T) {
	page, err := htmlmeta.Parse(strings.NewReader(substackPage))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	item, err := (&Extractor{}).Extract("https://patrick.substack.com/p/how-to-ship", page)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if item.Source != "substack" {
		t.Errorf("source: %q", item.Source)
	}
	if !strings.Contains(item.Content, "first step is shipping") {
		t.Errorf("article body missing: %q", item.Content)
	}
	for _, leak := range []string{"SUBSTACK NAV", "SIGN UP", "FOOTER"} {
		if strings.Contains(item.Content, leak) {
			t.Errorf("nav/footer leaked into body: %q", item.Content)
		}
	}
	if item.Extra["subtitle"] != "A short essay on shipping software" {
		t.Errorf("subtitle: %v", item.Extra["subtitle"])
	}
	if item.Extra["likes"] != "142" {
		t.Errorf("likes: %v", item.Extra["likes"])
	}
	if item.Extra["comment_count"] != "37" {
		t.Errorf("comment_count: %v", item.Extra["comment_count"])
	}
}

func TestSubstackExtractorMatch(t *testing.T) {
	ex := &Extractor{}
	cases := map[string]bool{
		"substack.com":             true,
		"stratechery.substack.com": true,
		"example.com":              false,
		"medium.com":               false,
	}
	for host, want := range cases {
		if got := ex.Match(host); got != want {
			t.Errorf("Match(%q) = %v, want %v", host, got, want)
		}
	}
}
