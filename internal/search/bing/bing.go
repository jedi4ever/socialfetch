// Package bing implements a search.Provider backed by Microsoft's Bing
// Web Search API.
//
// Auth: requires a subscription key passed via the Ocp-Apim-Subscription-Key
// header. Set BING_API_KEY in the environment, or assign it to Provider.Key
// directly. Microsoft hosts the API under several endpoints depending on
// your subscription tier; we default to the v7.0 endpoint and let callers
// override via BaseURL.
package bing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"
)

// Provider queries Bing's web search API.
type Provider struct {
	BaseURL string
	Key     string
	Market  string // e.g. "en-US"; empty = let Bing decide
}

func New() *Provider {
	return &Provider{
		BaseURL: "https://api.bing.microsoft.com/v7.0/search",
		Market:  "en-US",
	}
}

func (Provider) Name() string { return "bing" }

// response models the small slice of Bing's payload we care about. The
// real schema is much wider — this works for both the standard Web Search
// API and the smaller "Custom Search" endpoint.
type response struct {
	WebPages struct {
		Value []struct {
			Name    string `json:"name"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"value"`
	} `json:"webPages"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (p *Provider) Search(ctx context.Context, query string, max int) ([]search.Result, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("BING_API_KEY")
	}
	if key == "" {
		return nil, errors.New("bing: BING_API_KEY not set; pass --search-key or set the env var")
	}
	if max <= 0 {
		max = 10
	}
	if max > 50 {
		max = 50 // Bing's per-call cap
	}

	q := url.Values{
		"q":         {query},
		"count":     {fmt.Sprint(max)},
		"textDecorations": {"false"},
		"textFormat": {"Raw"},
	}
	if p.Market != "" {
		q.Set("mkt", p.Market)
	}
	endpoint := p.BaseURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", key)
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bing: HTTP %d", resp.StatusCode)
	}

	var body response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("bing: decode: %w", err)
	}
	if len(body.Errors) > 0 {
		return nil, fmt.Errorf("bing: %s", body.Errors[0].Message)
	}

	out := make([]search.Result, 0, len(body.WebPages.Value))
	for _, r := range body.WebPages.Value {
		if len(out) >= max {
			break
		}
		out = append(out, search.Result{
			Title:   r.Name,
			URL:     r.URL,
			Snippet: r.Snippet,
			Source:  "bing",
		})
	}
	return out, nil
}
