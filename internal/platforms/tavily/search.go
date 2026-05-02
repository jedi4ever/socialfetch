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
	"time"

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

func (p *Provider) Search(ctx context.Context, query string, opts search.Options) ([]search.Result, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("TAVILY_API_KEY")
	}
	if key == "" {
		return nil, errors.New("tavily: TAVILY_API_KEY not set; pass --search-key or set the env var")
	}
	max := opts.Max
	if max <= 0 {
		max = 10
	}
	if max > 20 {
		max = 20 // Tavily's per-call cap
	}

	// Tavily exposes "days" as a rolling-window filter. If the caller
	// asks for After=T, we approximate as "last N days" since today.
	// Provider.Days remains as a per-Provider default; per-call options
	// win when set.
	days := p.Days
	if opts.After != nil {
		d := int(time.Since(*opts.After).Hours()/24) + 1
		if d > 0 {
			days = d
		}
	}

	// Tavily's upstream `days` filter is only honored when topic="news",
	// but switching to news tanks recall on non-news queries (people
	// names, evergreen pages). Default to "general" and rely on the
	// client-side post-filter below for strictness. Users who *want*
	// strict upstream filtering can set TAVILY_TOPIC=news or override
	// Provider.Topic — they trade recall for a guaranteed window.
	topic := pickFirst(p.Topic, os.Getenv("TAVILY_TOPIC"), "general")

	include := append([]string{}, p.IncludeDomains...)
	include = append(include, opts.IncludeDomains...)
	exclude := append([]string{}, p.ExcludeDomains...)
	exclude = append(exclude, opts.ExcludeDomains...)

	body, err := json.Marshal(request{
		APIKey:         key,
		Query:          query,
		SearchDepth:    pickFirst(p.Depth, "advanced"),
		Topic:          topic,
		MaxResults:     max,
		IncludeAnswer:  false,
		Days:           days,
		IncludeDomains: include,
		ExcludeDomains: exclude,
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
		return nil, fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("tavily: decode: %w", err)
	}

	results := make([]search.Result, 0, len(out.Results))
	for _, r := range out.Results {
		pub := parsePublished(r.PublishedDate)
		// Defensive client-side filter: Tavily's window can leak a few
		// older items, and `topic="news"` indexes a narrower set of sites
		// — drop anything clearly outside the requested range when we
		// have a date to compare. Results without a date are kept (we
		// can't prove they're stale).
		if pub != nil {
			if opts.After != nil && pub.Before(*opts.After) {
				continue
			}
			if opts.Before != nil && pub.After(*opts.Before) {
				continue
			}
		}
		results = append(results, search.Result{
			Title:     r.Title,
			URL:       r.URL,
			Snippet:   snippet(r.Content, 500),
			Source:    "tavily",
			Published: pub,
		})
	}
	return results, nil
}

// parsePublished accepts the formats Tavily emits in published_date
// (typically "2006-01-02" or RFC3339). Returns nil if the string is
// missing or unparseable.
func parsePublished(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
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
