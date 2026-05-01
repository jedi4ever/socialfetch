package article

import (
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/htmlmd"
	"github.com/patrickdebois/social-skills/internal/htmlmeta"
)

// baseFromPage builds the part of an Item that's identical across all
// hosts: title/description/author/published/canonical/tags pulled from
// og: tags and JSON-LD. Per-host extractors call this first and then
// override or augment specific fields.
//
// The returned Item has FetchedAt set, an empty Content, no Media, no
// host-specific extras. Caller fills those in.
func baseFromPage(rawURL string, page *htmlmeta.Page, sourceName string) *core.Item {
	title := pickFirst(page.Meta["og:title"], page.Title)
	desc := pickFirst(page.Meta["og:description"], page.Meta["description"])
	author := pickFirst(page.Meta["author"], page.Meta["article:author"])
	canonical := pickFirst(page.CanonicalURL, page.Meta["og:url"], rawURL)
	siteName := page.Meta["og:site_name"]
	hero := page.Meta["og:image"]
	published := pickDate(page.Meta["article:published_time"], page.Meta["date"], page.Meta["pubdate"])

	for _, ld := range page.LDJSON {
		if author == "" {
			author = stringFromLD(ld["author"])
		}
		if published == nil {
			published = pickDate(asString(ld["datePublished"]))
		}
		if desc == "" {
			desc = asString(ld["description"])
		}
		if title == "" {
			title = asString(ld["headline"])
		}
	}

	media := []core.Media{}
	if hero != "" {
		media = append(media, core.Media{URL: hero, Type: "image"})
	}

	tags := splitTags(page.Meta["article:tag"], page.Meta["keywords"])

	return &core.Item{
		Source:      sourceName,
		Kind:        "article",
		URL:         canonical,
		CanonicalID: canonical,
		Title:       title,
		Author:      author,
		Published:   published,
		Summary:     desc,
		Tags:        tags,
		Media:       media,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"site_name":     siteName,
			"requested_url": rawURL,
		},
	}
}

// renderArticle picks the best article container using the given
// selector list, runs it through htmlmd, and returns clean markdown.
// Falls back to the description when no usable HTML is found.
func renderArticle(page *htmlmeta.Page, selectors []string, fallback string) string {
	html := htmlmeta.PickArticleHTML(page.Doc, selectors)
	md := strings.TrimSpace(htmlmd.Convert(html))
	if md == "" {
		return fallback
	}
	return md
}

// ---- small helpers (also used by per-host extractors) -----------------

func pickFirst(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func pickDate(values ...string) *time.Time {
	for _, v := range values {
		if v == "" {
			continue
		}
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02T15:04:05Z",
			"2006-01-02",
			time.RFC1123,
		} {
			if t, err := time.Parse(layout, v); err == nil {
				u := t.UTC()
				return &u
			}
		}
	}
	return nil
}

func stringFromLD(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		return asString(x["name"])
	case []any:
		if len(x) > 0 {
			return stringFromLD(x[0])
		}
	}
	return ""
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func splitTags(values ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if v == "" {
			continue
		}
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}
