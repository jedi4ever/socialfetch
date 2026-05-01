// Package serpapi implements a search.Provider backed by SerpAPI
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

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"
)

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

func (p *Provider) Search(ctx context.Context, query string, max int) ([]search.Result, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("SERPAPI_KEY")
	}
	if key == "" {
		return nil, errors.New("serpapi: SERPAPI_KEY not set; pass --search-key or set the env var")
	}
	if max <= 0 {
		max = 10
	}

	engine := p.Engine
	if engine == "" {
		engine = "google"
	}

	q := url.Values{
		"q":       {query},
		"engine":  {engine},
		"api_key": {key},
		"num":     {fmt.Sprint(max)},
	}
	endpoint := p.BaseURL + "?" + q.Encode()

	var resp response
	if err := core.GetJSON(ctx, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("serpapi: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("serpapi: %s", resp.Error)
	}

	results := make([]search.Result, 0, len(resp.OrganicResults))
	for _, r := range resp.OrganicResults {
		if len(results) >= max {
			break
		}
		results = append(results, search.Result{
			Title:   r.Title,
			URL:     r.Link,
			Snippet: r.Snippet,
			Source:  "serpapi",
		})
	}
	return results, nil
}
