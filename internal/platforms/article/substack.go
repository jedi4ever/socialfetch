package article

import (
	"strings"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/htmlmeta"
)

// substackArticleSelectors lists Substack's article body containers in
// priority order. ".body.markup" is the rendered prose; ".available-content"
// is what readers see before the paywall on locked posts.
var substackArticleSelectors = []string{
	"div.body.markup",
	".body.markup",
	".available-content",
	".post-content",
	"article",
}

// SubstackExtractor handles substack.com and any subdomain (newsletters
// are usually `name.substack.com`, but custom domains exist too — those
// fall back to the generic extractor unless someone wires a CNAME map).
type SubstackExtractor struct{}

func (*SubstackExtractor) Name() string { return "substack" }

func (*SubstackExtractor) Match(host string) bool {
	return host == "substack.com" || strings.HasSuffix(host, ".substack.com")
}

func (s *SubstackExtractor) Extract(rawURL string, page *htmlmeta.Page) (*core.Item, error) {
	item := baseFromPage(rawURL, page, "substack")
	item.Content = renderArticle(page, substackArticleSelectors, item.Summary)

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
