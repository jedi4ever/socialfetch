package linkedin

import (
	"encoding/json"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/jedi4ever/social-skills/internal/core"
)

// extractHeadless turns LinkedIn's anonymous guest-preview HTML into
// a *core.Item. Different shape from the bridge path:
//
//   - bridge HTML is already trimmed to the post container by the
//     extension's get_html — cleanHTML's selector-walking finds
//     `update-components-text` etc.
//   - headless / chromedp HTML is the FULL rendered guest-preview
//     page (sign-in chrome, "Agree & Join", related-posts sidebar,
//     etc.). The actual post body lives in a different place: in
//     the page's LD+JSON `articleBody` field (DiscussionForumPosting
//     / SocialMediaPosting), with og:tags as a metadata fallback.
//
// This function is the headless-specific counterpart to cleanHTML.
// It mirrors the layered approach in patai/providers/linkedin/
// downloader.py:
//
//  1. LD+JSON for the canonical post body, author, dates.
//  2. og: meta tags for title / hero image / description fallbacks.
//  3. DOM fallback: og:title pattern "<title> | <Author> posted on
//     the topic | LinkedIn" supplies author when LD+JSON doesn't.
//
// Returns a populated Item with empty Comments — the anonymous
// guest preview never includes the comment thread.
func extractHeadless(rawHTML, requestURL string) *core.Item {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		// Fail soft: we still have the URL + minimal Item shape.
		return &core.Item{
			Source:      "linkedin",
			Kind:        kindFor(requestURL),
			URL:         requestURL,
			CanonicalID: canonicalID(requestURL),
			FetchedAt:   time.Now().UTC(),
			Extra:       map[string]any{"via": "headless", "comment_count": 0},
		}
	}

	ld := firstLDJSON(doc)
	og := metaTags(doc)

	title := pickFirst(
		stringFromLD(ld, "headline"),
		og["og:title"],
		strings.TrimSpace(textOf(findFirst(doc, isTag(atom.Title)))),
	)
	body := pickFirst(
		stringFromLD(ld, "articleBody"),
		og["og:description"],
	)
	author, authorURL := authorFromLD(ld)
	if author == "" {
		// og:title shape: "Post first words… | Author Name posted
		// on the topic | LinkedIn". Fall through to the same
		// regex-based parser the jina path uses.
		author = parseAuthorFromTitle(title)
	}
	if author != "" {
		// Trim "| Author Name | LinkedIn" tail so the visible Title
		// is the post's first words rather than chrome.
		if i := strings.Index(title, " | "); i > 0 {
			title = strings.TrimSpace(title[:i])
		}
	}

	published := parseLDTime(stringFromLD(ld, "datePublished"))

	hero := og["og:image"]
	var media []core.Media
	if hero != "" {
		media = append(media, core.Media{URL: hero, Type: "image"})
	}

	return &core.Item{
		Source:      "linkedin",
		Kind:        kindFor(requestURL),
		URL:         requestURL,
		CanonicalID: canonicalID(requestURL),
		Title:       firstLine(title, 120),
		Author:      author,
		AuthorURL:   authorURL,
		Content:     strings.TrimSpace(body),
		Published:   published,
		Media:       media,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"via":           "headless",
			"comment_count": 0,
			"anonymous":     true,
		},
	}
}

// firstLDJSON walks the document for `<script type="application/ld+json">`
// blocks, parses the first one as a generic map, and returns it.
// Returns nil when the page has no LD+JSON or it's all unparseable.
//
// LinkedIn's guest-preview page typically has 1-3 LD+JSON blocks; the
// first is the post itself (DiscussionForumPosting). We don't filter by
// @type because LinkedIn has used several types over time (Article,
// SocialMediaPosting, DiscussionForumPosting) and any of them carries
// the fields we care about.
func firstLDJSON(n *html.Node) map[string]any {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && n.Data == "script" {
		if attr(n, "type") == "application/ld+json" {
			raw := strings.TrimSpace(textOf(n))
			if raw != "" {
				var m map[string]any
				if err := json.Unmarshal([]byte(raw), &m); err == nil && len(m) > 0 {
					return m
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := firstLDJSON(c); r != nil {
			return r
		}
	}
	return nil
}

// metaTags collects every `<meta name|property="X" content="Y">` into
// a map keyed by name/property. Property wins when both are set
// (matches og:* behaviour). Multi-value tags (article:tag) get
// silently overwritten — we only need single-valued lookups for the
// guest-preview extractor.
func metaTags(n *html.Node) map[string]string {
	out := map[string]string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "meta" {
			key := attr(n, "property")
			if key == "" {
				key = attr(n, "name")
			}
			if key != "" {
				if c := attr(n, "content"); c != "" {
					out[key] = c
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// stringFromLD pulls a string value from an LD+JSON map, including
// the common "field is wrapped in @graph or array" shapes:
//
//	{"articleBody": "..."}                      → direct
//	{"@graph": [{"articleBody": "..."}, ...]}   → first @graph entry
//	{"@list":  [{"articleBody": "..."}]}        → first @list entry
//
// Returns empty string when the key isn't present in any of those
// shapes.
func stringFromLD(ld map[string]any, key string) string {
	if ld == nil {
		return ""
	}
	if v, ok := ld[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	for _, container := range []string{"@graph", "@list"} {
		if list, ok := ld[container].([]any); ok {
			for _, e := range list {
				if m, ok := e.(map[string]any); ok {
					if s := stringFromLD(m, key); s != "" {
						return s
					}
				}
			}
		}
	}
	return ""
}

// authorFromLD extracts (display name, profile URL) from LD+JSON.
// LinkedIn uses several shapes for the author field — handle all
// the common ones:
//
//	{"author": "Cole Medin"}
//	{"author": {"name": "Cole Medin", "url": "https://..."}}
//	{"author": [{"name": "Cole Medin"}]}
//	{"author": {"@type": "Person", "name": "Cole Medin"}}
//
// Returns empty strings when the field is missing or "LinkedIn"
// (the generic-author fallback LinkedIn injects on ugcPosts —
// that's not a real author and the caller falls through to title
// parsing).
func authorFromLD(ld map[string]any) (name, url string) {
	if ld == nil {
		return "", ""
	}
	v, ok := ld["author"]
	if !ok {
		// Try @graph wrapper.
		if list, ok := ld["@graph"].([]any); ok {
			for _, e := range list {
				if m, ok := e.(map[string]any); ok {
					if n, u := authorFromLD(m); n != "" {
						return n, u
					}
				}
			}
		}
		return "", ""
	}
	switch x := v.(type) {
	case string:
		if !strings.EqualFold(x, "linkedin") {
			return strings.TrimSpace(x), ""
		}
	case map[string]any:
		n, _ := x["name"].(string)
		u, _ := x["url"].(string)
		if !strings.EqualFold(strings.TrimSpace(n), "linkedin") {
			return strings.TrimSpace(n), strings.TrimSpace(u)
		}
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				n, _ := m["name"].(string)
				u, _ := m["url"].(string)
				if n != "" && !strings.EqualFold(strings.TrimSpace(n), "linkedin") {
					return strings.TrimSpace(n), strings.TrimSpace(u)
				}
			} else if s, ok := e.(string); ok && !strings.EqualFold(s, "linkedin") {
				return strings.TrimSpace(s), ""
			}
		}
	}
	return "", ""
}

// parseLDTime parses LD+JSON datePublished. RFC3339 is the canonical
// format; we accept a couple of common variants and return nil for
// anything unrecognised so the Item just lacks Published rather than
// erroring out.
func parseLDTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

// --- tiny DOM helpers ---------------------------------------------
//
// attr / textOf / findFirst are re-used from fetch_extract.go; this
// file only adds the LD+JSON-aware predicate `isTag` so callers can
// pass `findFirst(doc, isTag(atom.Title))` instead of writing a
// closure at every call site.

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func isTag(tag atom.Atom) func(*html.Node) bool {
	return func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.DataAtom == tag
	}
}
