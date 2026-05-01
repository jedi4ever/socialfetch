// Package tavily implements a search.Provider backed by Tavily
// (https://tavily.com), an AI-tuned web search API. Tavily de-prioritises
// SEO listicles, returns a relevance score per result, and supports
// domain include/exclude as first-class parameters — better signal for
// agent retrieval than a generic search engine.
//
// Auth: TAVILY_API_KEY environment variable (or set Provider.Key).
package tavily

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"
)

// Provider configures the Tavily client. Most callers only need to set
// Key (or rely on TAVILY_API_KEY); the rest have sensible defaults.
type Provider struct {
	BaseURL        string
	Key            string
	Depth          string   // "basic" or "advanced"; default "advanced"
	Topic          string   // "general" or "news"; default "general"
	Days           int      // restrict to last N days, 0 = no limit
	IncludeDomains []string // allowlist
	ExcludeDomains []string // denylist
}

func New() *Provider {
	return &Provider{
		BaseURL: "https://api.tavily.com/search",
		Depth:   "advanced",
		Topic:   "general",
	}
}

func (Provider) Name() string { return "tavily" }

type request struct {
	APIKey         string   `json:"api_key"`
	Query          string   `json:"query"`
	SearchDepth    string   `json:"search_depth"`
	Topic          string   `json:"topic"`
	MaxResults     int      `json:"max_results"`
	IncludeAnswer  bool     `json:"include_answer"`
	Days           int      `json:"days,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
}

type response struct {
	Answer  string `json:"answer"`
	Results []struct {
		Title         string  `json:"title"`
		URL           string  `json:"url"`
		Content       string  `json:"content"`
		Score         float64 `json:"score"`
		PublishedDate string  `json:"published_date"`
	} `json:"results"`
}

func (p *Provider) Search(ctx context.Context, query string, max int) ([]search.Result, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("TAVILY_API_KEY")
	}
	if key == "" {
		return nil, errors.New("tavily: TAVILY_API_KEY not set; pass --search-key or set the env var")
	}
	if max <= 0 {
		max = 10
	}
	if max > 20 {
		max = 20 // Tavily's per-call cap
	}

	body, err := json.Marshal(request{
		APIKey:         key,
		Query:          query,
		SearchDepth:    pickFirst(p.Depth, "advanced"),
		Topic:          pickFirst(p.Topic, "general"),
		MaxResults:     max,
		IncludeAnswer:  false,
		Days:           p.Days,
		IncludeDomains: p.IncludeDomains,
		ExcludeDomains: p.ExcludeDomains,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tavily: HTTP %d", resp.StatusCode)
	}

	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("tavily: decode: %w", err)
	}

	results := make([]search.Result, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, search.Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: snippet(r.Content, 500),
			Source:  "tavily",
		})
	}
	return results, nil
}

func pickFirst(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func snippet(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
