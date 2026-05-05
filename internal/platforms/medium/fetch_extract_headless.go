package medium

import (
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/platforms/article"
	"github.com/jedi4ever/social-skills/internal/util/htmlmeta"
)

// extractHeadless turns a chromedp-rendered Medium page into a
// *core.Item. Different from MediumExtractor because chromedp's
// guest-preview DOM doesn't have Medium's modern article wrappers
// (`section.pw-post-body`, `article.meteredContent`) — those appear
// only in the logged-in / hydrated state. The headless DOM has a
// plain `<article>` whose paragraphs sit inside deeply-nested divs
// that the existing selector chain falls past, ending up matching
// `<body>` and grabbing the page chrome (sitemap link, "Open in
// app" CTA) instead of the article prose.
//
// Strategy:
//
//  1. Title / author / hero image / dates from og: tags + LD+JSON
//     — these are server-rendered and reliable in either DOM shape.
//  2. Body: walk the `<article>` element directly, picking up every
//     `<p>`, `<h2>`, `<blockquote>`, `<pre>`, `<li>` — same approach
//     the Python downloader's _extract_article_content uses.
//
// Returns nil-error even when fields are partially missing — best-
// effort, mirrors the headless extractors in linkedin/.
func extractHeadless(rawHTML, requestURL string) *core.Item {
	page, err := htmlmeta.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return &core.Item{
			Source:      "medium",
			Kind:        "article",
			URL:         requestURL,
			CanonicalID: requestURL,
			FetchedAt:   time.Now().UTC(),
			Extra:       map[string]any{"via": "headless"},
		}
	}

	// Reuse article.BaseFromPage for the og: + JSON-LD common
	// metadata pickup — Medium decorates its pages with the same
	// schema.org Article shape that the article package's extractor
	// already understands.
	item := article.BaseFromPage(requestURL, page, "medium")

	// Body: walk the <article> element. Medium's anti-bot
	// occasionally serves a degraded response with no article
	// element — when that happens we fall through to og:description
	// (Summary) so the Item still carries SOME body, rather than
	// going silent. Operators see the degradation in the audit log
	// (Content shorter than expected).
	if body := extractArticleBody(page); body != "" {
		item.Content = body
	} else if item.Summary != "" {
		item.Content = item.Summary
	}

	// Body images via the same helper the bridge path uses — chromedp
	// HTML preserves the same `miro.medium.com` / `cdn-images-1.medium.com`
	// hosts so the image filter still works.
	article.AppendBodyImages(item, page, []string{"article", "section.pw-post-body"}, mediumImageHost)

	if item.Extra == nil {
		item.Extra = map[string]any{}
	}
	item.Extra["via"] = "headless"
	item.Extra["anonymous"] = true
	return item
}

// extractArticleBody walks the first <article> element in the page
// and assembles a markdown body from its paragraph-level children.
// Same approach as patai/providers/browser_common.py's
// extract_article_content: pick up p / h1-h6 / blockquote / pre / li
// in document order, map to markdown, drop empties.
//
// Returns "" when no <article> is found OR when it's empty — caller
// falls back to the description.
func extractArticleBody(page *htmlmeta.Page) string {
	art := htmlmeta.SelectFirst(page.Doc, "article")
	if art == nil {
		return ""
	}
	wantedTags := map[string]bool{
		"p": true, "h1": true, "h2": true, "h3": true,
		"h4": true, "h5": true, "h6": true,
		"blockquote": true, "pre": true, "li": true,
	}
	var parts []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && wantedTags[n.Data] {
			text := strings.TrimSpace(collapseWS(htmlmeta.TextOf(n)))
			if text != "" {
				switch n.Data {
				case "h1", "h2", "h3", "h4", "h5", "h6":
					parts = append(parts, "## "+text)
				case "blockquote":
					parts = append(parts, "> "+text)
				case "pre":
					parts = append(parts, "```\n"+text+"\n```")
				case "li":
					parts = append(parts, "- "+text)
				default:
					parts = append(parts, text)
				}
			}
			// Don't recurse into matched elements — their inner
			// `<p>` / `<li>` etc. are part of the parent block.
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(art)
	return strings.Join(parts, "\n\n")
}

// collapseWS turns runs of whitespace into a single space — Medium's
// rendered DOM has lots of stray newlines between inline spans that
// we don't want bleeding into the markdown.
func collapseWS(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}
