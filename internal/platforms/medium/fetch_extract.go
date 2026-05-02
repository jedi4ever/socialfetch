package medium

import (
	"strings"

	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/platforms/article"
	"github.com/jedi4ever/socialfetch/internal/util/htmlmeta"
)

// articleSelectors lists the containers Medium articles use, in
// priority order. ".pw-post-body-paragraph" is the modern container;
// "article" and "[data-post-id]" cover legacy and minimal layouts.
var articleSelectors = []string{
	"section.pw-post-body",
	"article.meteredContent",
	"article",
	".postArticle-content",
	"[data-post-id]",
}

// Extractor is tuned for medium.com (and sub-domains like
// `username.medium.com`, `engineering.medium.com`, custom publication
// domains delegated to Medium). It uses Medium-specific selectors for
// the article body and pulls clap/response counts from the byline UI.
//
// Lives in this package so it can sit alongside the bridge-aware
// fetcher that uses it. The article package's catch-all fetcher no
// longer dispatches to a Medium extractor — medium.com URLs always
// route through this package's Fetcher first.
type Extractor struct{}

func (*Extractor) Name() string { return "medium" }

func (*Extractor) Match(host string) bool {
	return host == "medium.com" ||
		strings.HasSuffix(host, ".medium.com")
}

func (m *Extractor) Extract(rawURL string, page *htmlmeta.Page) (*core.Item, error) {
	item := article.BaseFromPage(rawURL, page, "medium")
	item.Content = article.RenderArticle(page, articleSelectors, item.Summary)

	// Medium-specific extras live on the byline / footer UI. All optional
	// — missing values don't break the item.
	if rt := page.Meta["twitter:data1"]; rt != "" {
		item.Extra["reading_time"] = rt
	}
	if pub := page.Meta["og:site_name"]; pub != "" && pub != "Medium" {
		item.Extra["publication"] = pub
	}
	if claps := htmlmeta.SelectInnerHTML(page.Doc, `button[data-testid=clapCount]`); claps != "" {
		item.Extra["clap_count"] = strings.TrimSpace(stripTags(claps))
	} else if n := htmlmeta.SelectFirst(page.Doc, ".pw-multi-vote-count"); n != nil {
		item.Extra["clap_count"] = strings.TrimSpace(htmlmeta.TextOf(n))
	}
	if n := htmlmeta.SelectFirst(page.Doc, `button[data-testid=responseCount]`); n != nil {
		item.Extra["response_count"] = strings.TrimSpace(htmlmeta.TextOf(n))
	}

	return item, nil
}

// stripTags is a tiny helper for inner HTML that may contain <span> or
// SVGs inside a button. We only need the visible number.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteByte(s[i])
			}
		}
	}
	return b.String()
}
