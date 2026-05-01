// Package arxivsearch implements a search.Provider backed by arXiv's
// public Atom search API. No auth.
//
// arXiv's query syntax is field-prefixed: `all:foo`, `ti:foo`,
// `au:smith`, `cat:cs.LG`. We pass the user query through as-is into
// `search_query` so power users can craft precise queries; bare words
// default to all-fields search via `all:`.
package arxivsearch

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"
)

const defaultBase = "https://export.arxiv.org/api/query"

type Provider struct {
	BaseURL string
}

func New() *Provider { return &Provider{BaseURL: defaultBase} }

func (Provider) Name() string { return "arxiv" }

type atomFeed struct {
	Entries []struct {
		ID        string `xml:"id"`
		Title     string `xml:"title"`
		Summary   string `xml:"summary"`
		Published string `xml:"published"`
		Updated   string `xml:"updated"`
		Authors   []struct {
			Name string `xml:"name"`
		} `xml:"author"`
	} `xml:"entry"`
}

func (p *Provider) Search(ctx context.Context, query string, opts search.Options) ([]search.Result, error) {
	maxN := opts.Max
	if maxN <= 0 {
		maxN = 10
	}
	if maxN > 50 {
		maxN = 50
	}

	// If the user didn't field-prefix their query (e.g. "ti:..."),
	// default to all-fields search.
	q := strings.TrimSpace(query)
	if !strings.ContainsAny(q, ":+") {
		q = "all:" + q
	}

	v := url.Values{
		"search_query": {q},
		"max_results":  {strconv.Itoa(maxN)},
		"sortBy":       {"submittedDate"},
		"sortOrder":    {"descending"},
	}
	base := p.BaseURL
	if base == "" {
		base = defaultBase
	}
	body, err := core.GetBytes(ctx, base+"?"+v.Encode())
	if err != nil {
		return nil, fmt.Errorf("arxiv search: %w", err)
	}
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("arxiv search: parse atom: %w", err)
	}

	out := make([]search.Result, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		// e.ID looks like https://arxiv.org/abs/2403.04132v1 — strip
		// the version suffix for canonical URLs.
		webURL := strings.Replace(e.ID, "http://", "https://", 1)
		// arxiv API returns id as http://arxiv.org/abs/...
		// Apply After/Before filters since the API has no native
		// date param — sort=descending lets us short-circuit cheaply.
		published := parseTime(e.Published)
		if opts.After != nil && published != nil && published.Before(*opts.After) {
			continue
		}
		if opts.Before != nil && published != nil && published.After(*opts.Before) {
			continue
		}
		authors := make([]string, 0, len(e.Authors))
		for _, a := range e.Authors {
			if n := strings.TrimSpace(a.Name); n != "" {
				authors = append(authors, n)
			}
		}
		snippet := cleanWhitespace(e.Summary)
		if as := strings.Join(authors, ", "); as != "" {
			snippet = as + " — " + snippet
		}
		out = append(out, search.Result{
			Title:     cleanWhitespace(e.Title),
			URL:       webURL,
			Snippet:   snippet,
			Source:    "arxiv",
			Published: published,
		})
	}
	return out, nil
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

func cleanWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
