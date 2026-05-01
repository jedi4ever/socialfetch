// Package article handles HTML article pages — Medium, Substack, blog
// posts, news sites, and anything else that exposes useful Open Graph or
// schema.org metadata. It's the catch-all fetcher: it claims any http(s)
// URL not already grabbed by a more specific source.
package article

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/htmlmd"
	"github.com/patrickdebois/social-skills/internal/htmlmeta"
)

// Fetcher pulls a single web page and extracts its main article content,
// metadata, and media references.
type Fetcher struct{}

func New() *Fetcher { return &Fetcher{} }

func (Fetcher) Name() string { return "article" }

// Match accepts any http(s) URL. Because the registry consults fetchers in
// order, this should be registered LAST so more specific fetchers
// (hackernews, reddit, github, twitter) get first dibs.
func (Fetcher) Match(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https")
}

func (Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	ctx = core.WithAudit(ctx, opts.Audit)
	body, err := core.GetBytes(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("article: %w", err)
	}
	page, err := htmlmeta.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("article: parse: %w", err)
	}

	source := classify(raw)
	title := pickFirst(page.Meta["og:title"], page.Title)
	desc := pickFirst(page.Meta["og:description"], page.Meta["description"])
	author := pickFirst(page.Meta["author"], page.Meta["article:author"])
	canonical := pickFirst(page.CanonicalURL, page.Meta["og:url"], raw)
	hero := page.Meta["og:image"]
	siteName := page.Meta["og:site_name"]

	published := pickDate(page.Meta["article:published_time"], page.Meta["date"], page.Meta["pubdate"])

	// LD+JSON often has the most reliable author/date. Fill in gaps from it.
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

	contentMD := strings.TrimSpace(htmlmd.Convert(page.ArticleHTML))
	if contentMD == "" {
		contentMD = desc
	}

	media := []core.Media{}
	if hero != "" {
		media = append(media, core.Media{URL: hero, Type: "image"})
	}

	tags := splitTags(page.Meta["article:tag"], page.Meta["keywords"])

	item := &core.Item{
		Source:      source,
		Kind:        "article",
		URL:         canonical,
		CanonicalID: canonical,
		Title:       title,
		Author:      author,
		Published:   published,
		Summary:     desc,
		Content:     contentMD,
		Tags:        tags,
		Media:       media,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"site_name":     siteName,
			"requested_url": raw,
		},
	}
	return item, nil
}

// classify guesses a source name from the host so downstream consumers can
// tell a Medium post from a generic page.
func classify(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "article"
	}
	host := strings.TrimPrefix(u.Host, "www.")
	switch {
	case strings.HasSuffix(host, "medium.com"), strings.HasSuffix(host, ".medium.com"):
		return "medium"
	case strings.HasSuffix(host, "substack.com"), strings.HasSuffix(host, ".substack.com"):
		return "substack"
	default:
		return "article"
	}
}

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

// stringFromLD extracts an author name out of LD+JSON's `author` field,
// which is variously a string, an object with "name", or an array.
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
