// Package arxiv fetches paper metadata + abstract from arXiv's public
// API (https://export.arxiv.org/api/query). The endpoint speaks
// Atom 1.0 and is unauthenticated.
//
// We claim:
//
//	arxiv.org/abs/<id>     → metadata page
//	arxiv.org/pdf/<id>     → PDF (we still pull metadata, not PDF text)
//	arxiv.org/html/<id>    → rendered HTML version (metadata path)
//
// IDs follow either the legacy hyphenated form (cs.LG/9301001) or the
// 2007+ "YYMM.NNNN" form (2403.04132); both are accepted.
package arxiv

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultAPIBase = "https://export.arxiv.org/api/query"

type Fetcher struct {
	BaseURL string
}

func New() *Fetcher { return &Fetcher{BaseURL: defaultAPIBase} }

func (Fetcher) Name() string { return "arxiv" }

// idRE matches both the post-2007 NNNN.NNNNN form and the legacy
// archive/category/yymm form. We accept an optional version suffix.
var idRE = regexp.MustCompile(`(?:[a-z\-]+(?:\.[A-Z]{2})?/[0-9]{7}|[0-9]{4}\.[0-9]{4,5})(v[0-9]+)?`)

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if host != "arxiv.org" && host != "export.arxiv.org" {
		return false
	}
	return strings.Contains(u.Path, "/abs/") ||
		strings.Contains(u.Path, "/pdf/") ||
		strings.Contains(u.Path, "/html/")
}

func extractID(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	path := u.Path
	for _, prefix := range []string{"/abs/", "/pdf/", "/html/"} {
		if i := strings.Index(path, prefix); i >= 0 {
			rest := path[i+len(prefix):]
			rest = strings.TrimSuffix(rest, ".pdf")
			rest = strings.TrimSuffix(rest, ".html")
			if id := idRE.FindString(rest); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("no arxiv id in %q", rawURL)
}

// atomFeed models the slice of arXiv's Atom output we read.
type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID         string       `xml:"id"`
	Title      string       `xml:"title"`
	Summary    string       `xml:"summary"`
	Published  string       `xml:"published"`
	Updated    string       `xml:"updated"`
	Authors    []atomAuthor `xml:"author"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
	Links []atomLink `xml:"link"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Rel   string `xml:"rel,attr"`
	Type  string `xml:"type,attr"`
	Href  string `xml:"href,attr"`
	Title string `xml:"title,attr"`
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	id, err := extractID(raw)
	if err != nil {
		return nil, fmt.Errorf("arxiv: %w", err)
	}
	ctx = core.WithAudit(ctx, opts.Audit)

	q := url.Values{"id_list": {id}}
	endpoint := f.BaseURL + "?" + q.Encode()
	opts.Audit.Logf("arxiv: GET %s", endpoint)

	body, err := core.GetBytes(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("arxiv: %w", err)
	}
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("arxiv: parse atom: %w", err)
	}
	if len(feed.Entries) == 0 {
		return nil, fmt.Errorf("arxiv: no entry returned for %q", id)
	}
	return entryToItem(feed.Entries[0], id), nil
}

func entryToItem(e atomEntry, id string) *core.Item {
	authors := make([]string, 0, len(e.Authors))
	for _, a := range e.Authors {
		if n := strings.TrimSpace(a.Name); n != "" {
			authors = append(authors, n)
		}
	}
	tags := make([]string, 0, len(e.Categories))
	for _, c := range e.Categories {
		if c.Term != "" {
			tags = append(tags, c.Term)
		}
	}
	pdfURL, htmlURL := "", ""
	for _, l := range e.Links {
		switch {
		case l.Rel == "related" && l.Title == "pdf":
			pdfURL = l.Href
		case l.Type == "text/html":
			htmlURL = l.Href
		}
	}
	if pdfURL == "" {
		pdfURL = "https://arxiv.org/pdf/" + id
	}
	if htmlURL == "" {
		htmlURL = "https://arxiv.org/abs/" + id
	}

	return &core.Item{
		Source:      "arxiv",
		Kind:        "paper",
		URL:         htmlURL,
		CanonicalID: id,
		Title:       cleanWhitespace(e.Title),
		Author:      strings.Join(authors, ", "),
		AuthorURL:   "",
		Published:   parseTime(e.Published),
		Summary:     cleanWhitespace(e.Summary),
		Content:     cleanWhitespace(e.Summary),
		Tags:        tags,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"pdf_url": pdfURL,
			"updated": e.Updated,
		},
	}
}

func cleanWhitespace(s string) string {
	// arXiv's Atom wraps abstracts at ~78 cols with newlines; collapse
	// to single spaces so the rendered markdown reads as prose.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
