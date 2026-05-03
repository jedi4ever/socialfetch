// Package arxivsearch implements a core.SearchProvider backed by arXiv's
// public Atom search API. No auth.
//
// arXiv's query syntax is field-prefixed: `all:foo`, `ti:foo`,
// `au:smith`, `cat:cs.LG`. Bare-word queries get rewritten so the
// terms AND together — arXiv's default for multi-token queries is
// surprisingly OR (so `harness engineering` matches every paper
// about harnesses *or* engineering, dominated by today's
// engineering submissions when the sort is by date), which is
// almost never what users expect when they type two words. Power
// users who explicitly write boolean operators or field prefixes
// get their query passed through unchanged.
package arxiv

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jedi4ever/social-skills/internal/core"
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

// buildArxivQuery rewrites a user query string into arXiv's
// search_query syntax. Three branches:
//
//  1. The query already looks "advanced" (contains a field prefix
//     like `ti:`, an explicit boolean operator, parens, or a phrase
//     quote) — pass through verbatim. Power users who care about
//     query precision get the literal arXiv DSL.
//
//  2. Single bare term — wrap in `all:` so the term searches across
//     every field (title, abstract, authors, comments, etc.).
//
//  3. Multi-word bare query — wrap each term in `all:` and join
//     with explicit ` AND `. Without this, arXiv's default OR
//     semantics + sortBy=submittedDate produces "today's papers
//     that mention any of these words" instead of "papers about
//     this topic", which is the bug that motivated this rewrite.
//
// Bare alphanumerics with hyphens (`harness-engineering`) get
// quoted as a phrase since arXiv's parser splits on the hyphen
// otherwise.
func buildArxivQuery(q string) string {
	if q == "" {
		return ""
	}
	if looksLikeArxivAdvancedQuery(q) {
		return q
	}
	parts := strings.Fields(q)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		// Hyphenated terms get phrase-quoted so arXiv treats
		// them as a single token rather than splitting on `-`.
		if strings.ContainsAny(p, "-") {
			p = `"` + p + `"`
		}
		out = append(out, "all:"+p)
	}
	return strings.Join(out, " AND ")
}

// looksLikeArxivAdvancedQuery decides whether a query string already
// uses arXiv's search DSL and should pass through unchanged. We
// recognize the common signals: a field prefix (anything with a
// colon — `ti:`, `abs:`, `au:`, `cat:`, `all:`, `id:`), explicit
// boolean operators (`AND`/`OR`/`NOT` as standalone tokens), parens
// for grouping, or quote-bracketed phrases. Anything else is a
// "bare-word" query that gets rewritten via buildArxivQuery.
func looksLikeArxivAdvancedQuery(q string) bool {
	if strings.ContainsAny(q, `:()`) {
		return true
	}
	if strings.Contains(q, `"`) {
		return true
	}
	upper := " " + strings.ToUpper(q) + " "
	return strings.Contains(upper, " AND ") ||
		strings.Contains(upper, " OR ") ||
		strings.Contains(upper, " NOT ")
}

func (p *SearchProvider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	maxN := opts.Max
	if maxN <= 0 {
		maxN = 10
	}
	if maxN > 50 {
		maxN = 50
	}

	q := buildArxivQuery(strings.TrimSpace(query))
	// Pick the sort order based on whether the caller cares about
	// recency. Bare topic queries default to `relevance` so the
	// best-matching paper is at the top regardless of submission date
	// (the original "Attention is All You Need" beats today's
	// preprints when searching `transformer`). When the caller passes
	// a date filter (--after / --before / --last) recency is the
	// whole point, so flip to `submittedDate` desc and rely on the
	// client-side filter below to drop anything outside the window.
	sortBy := "relevance"
	if opts.After != nil || opts.Before != nil {
		sortBy = "submittedDate"
	}

	v := url.Values{
		"search_query": {q},
		"max_results":  {strconv.Itoa(maxN)},
		"sortBy":       {sortBy},
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
