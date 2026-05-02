package article

import (
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/htmlmeta"
)

// genericArticleSelectors is the catch-all selector list — same set
// htmlmeta uses by default. Listed here so callers can inspect or extend
// it.
var genericArticleSelectors = []string{
	"article",
	"main",
	"[role=main]",
	".post-content",
	".entry-content",
	".article-body",
	".article-content",
	"#content",
	".content",
}

// GenericExtractor is the fallback for any host without a dedicated
// extractor. It uses the broadest selector list and adds no extras.
type GenericExtractor struct{}

func (*GenericExtractor) Name() string                { return "generic" }
func (*GenericExtractor) Match(host string) bool      { return true }

func (g *GenericExtractor) Extract(rawURL string, page *htmlmeta.Page) (*core.Item, error) {
	item := baseFromPage(rawURL, page, "article")
	item.Content = renderArticle(page, genericArticleSelectors, item.Summary)
	return item, nil
}
