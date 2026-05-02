// Package serpapi implements a core.SearchProvider backed by SerpAPI
// (https://serpapi.com). Requires a SERPAPI_KEY environment variable
// (or an explicit Key on the Provider).
//
// We model only the JSON fields we use; the response has many more.
package serpapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/patrickdebois/social-skills/internal/core"
)

func applyDomainOpsSerp(query string, opts core.SearchOptions) string {
	parts := []string{query}
	for _, d := range opts.IncludeDomains {
		parts = append(parts, "site:"+d)
	}
	for _, d := range opts.ExcludeDomains {
		parts = append(parts, "-site:"+d)
	}
	return strings.Join(parts, " ")
}

func serpDateRange(opts core.SearchOptions) string {
	if opts.After == nil && opts.Before == nil {
		return ""
	}
	min := ""
	max := ""
	if opts.After != nil {
		min = opts.After.UTC().Format("01/02/2006")
	}
	if opts.Before != nil {
		max = opts.Before.UTC().Format("01/02/2006")
	}
	parts := []string{"cdr:1"}
	if min != "" {
		parts = append(parts, "cd_min:"+min)
	}
	if max != "" {
		parts = append(parts, "cd_max:"+max)
	}
	return strings.Join(parts, ",")
}

// Provider queries SerpAPI's google engine.
type Provider struct {
	BaseURL string
	Key     string
	Engine  string // defaults to "google"
}

func New() *Provider {
	return &Provider{
		BaseURL: "https://serpapi.com/search.json",
		Engine:  "google",
	}
}

func (Provider) Name() string { return "serpapi" }

type response struct {
	OrganicResults []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"organic_results"`
	Error string `json:"error"`
}

func (p *Provider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("SERPAPI_KEY")
	}
	if key == "" {
		return nil, errors.New("serpapi: SERPAPI_KEY not set; pass --search-key or set the env var")
	}
	max := opts.Max
	if max <= 0 {
		max = 10
	}

	engine := p.Engine
	if engine == "" {
		engine = "google"
	}

	q := url.Values{
		"q":       {applyDomainOpsSerp(query, opts)},
		"engine":  {engine},
		"api_key": {key},
		"num":     {fmt.Sprint(max)},
	}
	// Google supports a `tbs=cdr:1,cd_min:MM/DD/YYYY,cd_max:MM/DD/YYYY`
	// custom-date-range filter that SerpAPI passes through unchanged.
	if tbs := serpDateRange(opts); tbs != "" {
		q.Set("tbs", tbs)
	}
	endpoint := p.BaseURL + "?" + q.Encode()

	var resp response
	if err := core.GetJSON(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("serpapi: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("serpapi: %s", resp.Error)
	}

	results := make([]core.SearchResult, 0, len(resp.OrganicResults))
	for _, r := range resp.OrganicResults {
		if len(results) >= max {
			break
		}
		results = append(results, core.SearchResult{
			Title:   r.Title,
			URL:     r.Link,
			Snippet: r.Snippet,
			Source:  "serpapi",
		})
	}
	return results, nil
}
