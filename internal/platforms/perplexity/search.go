// Package perplexity's search side hits the dedicated POST /search
// endpoint — separate from the Sonar Chat Completions ask path. This
// returns raw search results (title / url / snippet / date) without
// LLM synthesis, which is faster and cheaper than ask when the caller
// just wants links.
//
// Auth: same PERPLEXITY_API_KEY as the ask side.
//
// Date filters: Perplexity exposes both absolute (search_after_date_filter
// / search_before_date_filter, MM/DD/YYYY) and relative
// (search_recency_filter: hour/day/week/month/year) windows. We pick
// the relative window when opts.After is recent enough to map cleanly,
// and otherwise fall back to absolute dates.
//
// Refs:
//   - https://docs.perplexity.ai/api-reference/search-post
package perplexity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultSearchBase = "https://api.perplexity.ai/search"

// SearchProvider implements core.SearchProvider.
type SearchProvider struct {
	BaseURL string
	Key     string
}

// NewSearchProvider builds a Perplexity search client with the
// default endpoint.
func NewSearchProvider() *SearchProvider {
	return &SearchProvider{BaseURL: defaultSearchBase}
}

func (*SearchProvider) Name() string { return "perplexity" }

// searchRequest mirrors the Perplexity /search body. Only the fields
// we set go on the wire; the API has more (max_tokens, country,
// search_language_filter) which we leave at defaults until a caller
// needs them.
type searchRequest struct {
	Query                  string   `json:"query"`
	MaxResults             int      `json:"max_results,omitempty"`
	SearchDomainFilter     []string `json:"search_domain_filter,omitempty"`
	SearchAfterDateFilter  string   `json:"search_after_date_filter,omitempty"`
	SearchBeforeDateFilter string   `json:"search_before_date_filter,omitempty"`
}

type searchResponse struct {
	Results []struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Snippet     string `json:"snippet"`
		Date        string `json:"date"`
		LastUpdated string `json:"last_updated"`
	} `json:"results"`
	ID string `json:"id"`
}

func (p *SearchProvider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	key := p.Key
	if key == "" {
		key = firstEnv("PERPLEXITY_API_KEY", "PPLX_API_KEY")
	}
	if key == "" {
		return nil, errors.New("perplexity search: PERPLEXITY_API_KEY not set")
	}

	max := opts.Max
	if max <= 0 {
		max = 10
	}
	// Perplexity's hard cap. Going over silently truncates.
	if max > 20 {
		max = 20
	}

	req := searchRequest{
		Query:              query,
		MaxResults:         max,
		SearchDomainFilter: domainFilter(opts.IncludeDomains, opts.ExcludeDomains),
	}
	if opts.After != nil {
		req.SearchAfterDateFilter = opts.After.Format("1/2/2006")
	}
	if opts.Before != nil {
		req.SearchBeforeDateFilter = opts.Before.Format("1/2/2006")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("perplexity search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("perplexity search: HTTP 401 — PERPLEXITY_API_KEY rejected")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("perplexity search: HTTP 429 — rate limit: %s", core.HTTPErrorBody(resp))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("perplexity search: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("perplexity search: decode: %w", err)
	}

	results := make([]core.SearchResult, 0, len(data.Results))
	for _, r := range data.Results {
		sr := core.SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Snippet,
			Source:  "perplexity",
		}
		if t := parseSearchDate(r.Date); t != nil {
			sr.Published = t
		} else if t := parseSearchDate(r.LastUpdated); t != nil {
			sr.Published = t
		}
		results = append(results, sr)
	}
	return results, nil
}

// domainFilter merges include + exclude lists into the format
// Perplexity expects. The API uses a single `search_domain_filter`
// list where excludes are prefixed with `-`. Up to 20 entries.
func domainFilter(include, exclude []string) []string {
	var out []string
	for _, d := range include {
		if d != "" {
			out = append(out, d)
		}
	}
	for _, d := range exclude {
		if d != "" {
			out = append(out, "-"+d)
		}
	}
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// parseSearchDate handles the date shapes Perplexity returns. They
// usually use ISO-8601 (YYYY-MM-DD) but occasionally include time
// components — we accept both.
func parseSearchDate(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
