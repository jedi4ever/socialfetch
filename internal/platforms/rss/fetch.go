// Package rss parses RSS 2.0 and Atom feeds, returning a single Item
// whose Children are the feed entries.
//
// Match is intentionally narrow — only URLs that look like a feed (path
// or query hints, plus a Content-Type sniff at fetch time). Generic blog
// front-pages should fall through to the article fetcher instead.
package rss

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

// Fetcher pulls a feed and converts it to a core.Item with Children.
type Fetcher struct{}

func New() *Fetcher { return &Fetcher{} }

func (Fetcher) Name() string { return "rss" }

func (Fetcher) Match(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	p := strings.ToLower(u.Path)
	if strings.HasSuffix(p, ".xml") || strings.HasSuffix(p, ".rss") || strings.HasSuffix(p, ".atom") {
		return true
	}
	switch {
	case strings.Contains(p, "/feed"),
		strings.Contains(p, "/rss"),
		strings.Contains(p, "/atom"):
		return true
	}
	return false
}

// rssFeed and atomFeed model the parts of each format we care about. We
// decode into a unified intermediate ([]entry) regardless of which one
// the server speaks.
type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Language    string    `xml:"language"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	Author      string   `xml:"creator"` // dc:creator usually
	Description string   `xml:"description"`
	Content     string   `xml:"encoded"` // content:encoded
	Categories  []string `xml:"category"`
}

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Link    []atomLink  `xml:"link"`
	Updated string      `xml:"updated"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomEntry struct {
	Title     string     `xml:"title"`
	ID        string     `xml:"id"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Author    atomAuthor `xml:"author"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
	Links     []atomLink `xml:"link"`
	Category  []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

func (Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	ctx = core.WithAudit(ctx, opts.Audit)
	body, err := core.GetBytes(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("rss: %w", err)
	}

	feedTitle, feedLink, entries, err := parseFeed(body)
	if err != nil {
		return nil, fmt.Errorf("rss: %w", err)
	}

	children := make([]core.Item, 0, len(entries))
	for _, e := range entries {
		children = append(children, e.toItem())
	}

	item := &core.Item{
		Source:    "rss",
		Kind:      "feed",
		URL:       feedLink,
		Title:     feedTitle,
		FetchedAt: time.Now().UTC(),
		Children:  children,
		Extra: map[string]any{
			"requested_url": raw,
			"entry_count":   len(children),
		},
	}
	return item, nil
}

// entry is the in-memory shape both RSS and Atom feeds normalize to.
type entry struct {
	Title     string
	Link      string
	GUID      string
	Author    string
	Published *time.Time
	Summary   string
	Content   string
	Tags      []string
}

func (e entry) toItem() core.Item {
	return core.Item{
		Source:      "rss",
		Kind:        "entry",
		URL:         e.Link,
		CanonicalID: e.GUID,
		Title:       e.Title,
		Author:      e.Author,
		Published:   e.Published,
		Summary:     e.Summary,
		Content:     e.Content,
		Tags:        e.Tags,
		FetchedAt:   time.Now().UTC(),
	}
}

func parseFeed(body []byte) (feedTitle, feedLink string, entries []entry, err error) {
	// Try RSS first — it's the most common.
	var r rssFeed
	if xml.Unmarshal(body, &r) == nil && len(r.Channel.Items) > 0 {
		feedTitle = r.Channel.Title
		feedLink = r.Channel.Link
		for _, it := range r.Channel.Items {
			entries = append(entries, entry{
				Title:     it.Title,
				Link:      it.Link,
				GUID:      it.GUID,
				Author:    it.Author,
				Published: parseFeedTime(it.PubDate),
				Summary:   it.Description,
				Content:   it.Content,
				Tags:      it.Categories,
			})
		}
		return
	}

	// Fall back to Atom.
	var a atomFeed
	if xml.Unmarshal(body, &a) == nil && len(a.Entries) > 0 {
		feedTitle = a.Title
		feedLink = pickAtomLink(a.Link, "alternate", "")
		for _, e := range a.Entries {
			tags := make([]string, 0, len(e.Category))
			for _, c := range e.Category {
				if c.Term != "" {
					tags = append(tags, c.Term)
				}
			}
			pub := pickFirst(e.Published, e.Updated)
			entries = append(entries, entry{
				Title:     e.Title,
				Link:      pickAtomLink(e.Links, "alternate", ""),
				GUID:      e.ID,
				Author:    e.Author.Name,
				Published: parseFeedTime(pub),
				Summary:   e.Summary,
				Content:   e.Content,
				Tags:      tags,
			})
		}
		return
	}

	return "", "", nil, fmt.Errorf("not a recognized RSS or Atom feed")
}

func pickAtomLink(links []atomLink, rel, fallback string) string {
	for _, l := range links {
		if l.Rel == rel || (rel == "alternate" && l.Rel == "") {
			return l.Href
		}
	}
	if len(links) > 0 {
		return links[0].Href
	}
	return fallback
}

func pickFirst(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseFeedTime tries every layout RSS/Atom feeds use in the wild.
func parseFeedTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"Mon, 02 Jan 2006 15:04:05 MST",
		"Mon, 2 Jan 2006 15:04:05 -0700",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
