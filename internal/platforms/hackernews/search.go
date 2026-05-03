// Package hnsearch implements a core.SearchProvider backed by Algolia's free
// HackerNews search API at hn.algolia.com — no auth, returns stories,
// comments, polls. Same data the HN search box on news.ycombinator.com
// uses.
//
// Docs: https://hn.algolia.com/api
package hackernews

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/jedi4ever/social-skills/internal/core"
)

// lastIndex is strings.LastIndex; tiny alias for readability at call site.
func lastIndex(s, sub string) int { return strings.LastIndex(s, sub) }

// hnDateFilter builds a numericFilters string for the HN Algolia API.
// Multiple filters are joined with commas (AND semantics).
func hnDateFilter(opts core.SearchOptions) string {
	var parts []string
	if opts.After != nil {
		parts = append(parts, fmt.Sprintf("created_at_i>%d", opts.After.Unix()))
	}
	if opts.Before != nil {
		parts = append(parts, fmt.Sprintf("created_at_i<%d", opts.Before.Unix()))
	}
	return strings.Join(parts, ",")
}

// hnDomainQuery folds include/exclude domains into the query string.
// Algolia doesn't have a first-class domain filter, but HN indexes the
// URL host as a token so plain text-search works in practice.
func hnDomainQuery(query string, opts core.SearchOptions) string {
	parts := []string{query}
	for _, d := range opts.IncludeDomains {
		parts = append(parts, d)
	}
	for _, d := range opts.ExcludeDomains {
		parts = append(parts, "-"+d)
	}
	return strings.Join(parts, " ")
}

// Provider queries the Algolia HN search endpoint.
type SearchProvider struct {
	BaseURL string
	// SortByDate switches from default "by relevance" to "by date".
	// HN's search box has both modes; mirror the choice here.
	SortByDate bool
	// Tags restricts result types. Common values: "story", "comment",
	// "poll", "show_hn", "ask_hn", "front_page". Empty = all.
	Tags string
}

func NewSearchProvider() *SearchProvider {
	return &SearchProvider{
		BaseURL: "https://hn.algolia.com/api/v1/search",
		Tags:    "story", // most users searching HN want stories
	}
}

func (SearchProvider) Name() string { return "hackernews" }

type response struct {
	Hits []struct {
		ObjectID    string   `json:"objectID"`
		Title       string   `json:"title"`
		URL         string   `json:"url"`
		Author      string   `json:"author"`
		Points      int      `json:"points"`
		NumComments int      `json:"num_comments"`
		CreatedAt   string   `json:"created_at"`
		StoryText   string   `json:"story_text"`
		CommentText string   `json:"comment_text"`
		Tags        []string `json:"_tags"`
	} `json:"hits"`
}

func (p *SearchProvider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	max := opts.Max
	if max <= 0 {
		max = 10
	}
	if max > 100 {
		max = 100 // Algolia caps hits per page at 100
	}

	endpoint := p.BaseURL
	if p.SortByDate {
		// Algolia's HN API exposes /search and /search_by_date as
		// sibling endpoints. Swap the suffix on whatever BaseURL is
		// configured (so tests pointing at a fake server still work).
		if i := lastIndex(endpoint, "/search"); i >= 0 {
			endpoint = endpoint[:i] + "/search_by_date"
		}
	}

	q := url.Values{
		"query":       {query},
		"hitsPerPage": {fmt.Sprint(max)},
	}
	// Algolia exposes 0-indexed `page=N` pagination. Translate
	// opts.Start by dividing through hitsPerPage — for clean
	// alignment callers should pass Start as a multiple of max
	// (e.g. max=10 + start=0/10/20/...). Off-multiple Start values
	// land on the page that contains that offset; the in-page skip
	// is intentionally NOT applied because Algolia doesn't expose
	// a native offset and the hits-on-page alignment is a fast-
	// path good enough for the typical agent paging loop.
	if opts.Start > 0 {
		q.Set("page", fmt.Sprint(opts.Start/max))
	}
	if p.Tags != "" {
		q.Set("tags", p.Tags)
	}
	// HN Algolia takes Unix-seconds bounds on `created_at_i`. Domain
	// filters aren't a first-class param; translate them to inline
	// query operators (Algolia indexes the URL host) so include/exclude
	// still works.
	if filt := hnDateFilter(opts); filt != "" {
		q.Set("numericFilters", filt)
	}
	if dom := hnDomainQuery(query, opts); dom != query {
		q.Set("query", dom)
	}
	full := endpoint + "?" + q.Encode()

	var resp response
	if err := core.GetJSON(ctx, full, &resp); err != nil {
		return nil, fmt.Errorf("hackernews search: %w", err)
	}

	results := make([]core.SearchResult, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		title := h.Title
		// Comments don't have a title; surface the comment text instead.
		if title == "" {
			title = "comment by " + h.Author
		}
		// HN points the URL at the linked article when present, else at
		// the HN discussion page itself.
		hnURL := fmt.Sprintf("https://news.ycombinator.com/item?id=%s", h.ObjectID)
		linked := h.URL
		if linked == "" {
			linked = hnURL
		}
		snippet := h.StoryText
		if snippet == "" {
			snippet = h.CommentText
		}
		results = append(results, core.SearchResult{
			Title:   fmt.Sprintf("%s (%d points, %d comments)", title, h.Points, h.NumComments),
			URL:     linked,
			Snippet: snippet,
			Source:  "hackernews",
		})
	}
	return results, nil
}
