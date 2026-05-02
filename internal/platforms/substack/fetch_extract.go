package substack

import (
	"strings"

	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/platforms/article"
	"github.com/jedi4ever/socialfetch/internal/util/htmlmeta"
)

// articleSelectors lists Substack's article body containers in
// priority order. ".body.markup" is the rendered prose; ".available-content"
// is what readers see before the paywall on locked posts.
var articleSelectors = []string{
	"div.body.markup",
	".body.markup",
	".available-content",
	".post-content",
	"article",
}

// Extractor handles substack.com and any subdomain (newsletters
// are usually `name.substack.com`, but custom domains exist too — those
// fall back to the generic extractor unless someone wires a CNAME map).
//
// Lives in this package so it can sit alongside the bridge-aware
// fetcher that uses it. The article package's catch-all fetcher no
// longer dispatches to a Substack extractor — substack.com URLs always
// route through this package's Fetcher first.
type Extractor struct{}

func (*Extractor) Name() string { return "substack" }

func (*Extractor) Match(host string) bool {
	return host == "substack.com" || strings.HasSuffix(host, ".substack.com")
}

func (s *Extractor) Extract(rawURL string, page *htmlmeta.Page) (*core.Item, error) {
	item := article.BaseFromPage(rawURL, page, "substack")
	item.Content = article.RenderArticle(page, articleSelectors, item.Summary)

	// Substack-specific extras: subtitle, publication name, like &
	// comment counts. All optional.
	if n := htmlmeta.SelectFirst(page.Doc, "h3.subtitle-text"); n != nil {
		item.Extra["subtitle"] = strings.TrimSpace(htmlmeta.TextOf(n))
	} else if n := htmlmeta.SelectFirst(page.Doc, ".subtitle"); n != nil {
		item.Extra["subtitle"] = strings.TrimSpace(htmlmeta.TextOf(n))
	}
	if pub := page.Meta["og:site_name"]; pub != "" {
		item.Extra["publication"] = pub
	}
	if n := htmlmeta.SelectFirst(page.Doc, ".like-count"); n != nil {
		item.Extra["likes"] = strings.TrimSpace(htmlmeta.TextOf(n))
	}
	if n := htmlmeta.SelectFirst(page.Doc, ".post-ufi-comment-button"); n != nil {
		item.Extra["comment_count"] = strings.TrimSpace(htmlmeta.TextOf(n))
	}

	return item, nil
}
