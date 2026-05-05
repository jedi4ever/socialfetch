package medium

import (
	"strings"
	"testing"
)

// TestExtractHeadless covers the chromedp-shape extractor:
// `<article>` with deeply-nested divs and paragraph-level elements,
// plus standard og: meta tags. Locks in the contract that title +
// author + body all populate, plus the fail-soft path where the
// article element is missing (Medium anti-bot degradation case).
func TestExtractHeadless(t *testing.T) {
	t.Run("article with paragraphs", func(t *testing.T) {
		html := `<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="My Post">
  <meta property="og:description" content="A summary.">
  <meta property="og:url" content="https://medium.com/@alice/my-post-abc">
  <meta property="og:image" content="https://miro.medium.com/hero.jpg">
  <meta name="article:author" content="Alice Smith">
</head>
<body>
  <header><a href="/sitemap">Sitemap</a></header>
  <article>
    <div><div><div>
      <h1>My Post</h1>
      <p>This is the first paragraph.</p>
      <p>And here is a <a href="https://example.com">link</a>.</p>
      <h2>A section header</h2>
      <p>Second paragraph after the header.</p>
      <blockquote>A quote.</blockquote>
      <ul><li>one</li><li>two</li></ul>
    </div></div></div>
  </article>
  <footer>Copyright</footer>
</body>
</html>`
		item := extractHeadless(html, "https://medium.com/@alice/my-post-abc")
		if item.Title != "My Post" {
			t.Errorf("Title = %q", item.Title)
		}
		if item.Author != "Alice Smith" {
			t.Errorf("Author = %q", item.Author)
		}
		if !strings.Contains(item.Content, "first paragraph") {
			t.Errorf("Content missing body: %q", item.Content)
		}
		if !strings.Contains(item.Content, "## A section header") {
			t.Errorf("Content missing header: %q", item.Content)
		}
		if !strings.Contains(item.Content, "> A quote") {
			t.Errorf("Content missing blockquote: %q", item.Content)
		}
		if !strings.Contains(item.Content, "- one") {
			t.Errorf("Content missing list item: %q", item.Content)
		}
		// Footer + header chrome must NOT leak into the body — we
		// only walk `<article>`, so "Sitemap" and "Copyright" should
		// be absent.
		if strings.Contains(item.Content, "Sitemap") {
			t.Errorf("header chrome leaked: %q", item.Content)
		}
		if strings.Contains(item.Content, "Copyright") {
			t.Errorf("footer chrome leaked: %q", item.Content)
		}
		if v, _ := item.Extra["via"].(string); v != "headless" {
			t.Errorf("Extra[via] = %v, want headless", v)
		}
	})

	t.Run("no article element falls through to og:description", func(t *testing.T) {
		// Anti-bot degraded case: Medium serves a shell with
		// og:tags but no <article>. extractor should populate
		// title/author from og:tags and fall through Content to
		// Summary so the Item still has SOMETHING.
		html := `<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Degraded Post">
  <meta property="og:description" content="Excerpt of the post.">
  <meta name="article:author" content="Alice">
</head>
<body><div>Sign in to read</div></body>
</html>`
		item := extractHeadless(html, "https://medium.com/@alice/degraded")
		if item.Title != "Degraded Post" {
			t.Errorf("Title = %q", item.Title)
		}
		if item.Content != "Excerpt of the post." {
			t.Errorf("Content = %q, want fallback to Summary", item.Content)
		}
	})

	t.Run("collapseWS normalises stray whitespace", func(t *testing.T) {
		// Medium's chromedp render has lots of whitespace between
		// inline spans inside paragraphs — collapseWS turns runs of
		// whitespace into a single space.
		got := collapseWS("  hello\n\n   world\t\n  ")
		want := " hello world "
		if got != want {
			t.Errorf("collapseWS = %q, want %q", got, want)
		}
	})
}
