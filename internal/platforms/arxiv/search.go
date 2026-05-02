// Package arxivsearch implements a core.SearchProvider backed by arXiv's
// public Atom search API. No auth.
//
// arXiv's query syntax is field-prefixed: `all:foo`, `ti:foo`,
// `au:smith`, `cat:cs.LG`. We pass the user query through as-is into
// `search_query` so power users can craft precise queries; bare words
// default to all-fields search via `all:`.
package arxiv

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultBase = "https://export.arxiv.org/api/query"

type SearchProvider struct {
	BaseURL string
}

func NewSearchProvider() *SearchProvider { return &SearchProvider{BaseURL: defaultBase} }

func (SearchProvider) Name() string { return "arxiv" }

type searchAtomFeed struct {
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

func (p *SearchProvider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
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
	var feed searchAtomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("arxiv search: parse atom: %w", err)
	}

	out := make([]core.SearchResult, 0, len(feed.Entries))
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
		out = append(out, core.SearchResult{
			Title:     cleanWhitespace(e.Title),
			URL:       webURL,
			Snippet:   snippet,
			Source:    "arxiv",
			Published: published,
		})
	}
	return out, nil
}
