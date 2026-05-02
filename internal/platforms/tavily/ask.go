// Package tavily's Ask side adapts Tavily as an answer engine: a
// regular /search call with include_answer=true returns a synthesized
// answer alongside the result list, which is exactly the core.Answer
// shape.
//
// Auth: same TAVILY_API_KEY as the search provider.
//
// Limitations:
//   - opts.Model is ignored — Tavily's /search synthesises with their
//     own backend; no caller-controlled model.
//   - opts.Instructions is ignored — Tavily's /search has no
//     system-prompt parameter (their newer /answer endpoint does, but
//     it's a separate API). If you need instruction-following, use
//     -p perplexity / grok / google instead.
package tavily

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultAskBase = "https://api.tavily.com/search"

// longAskClient handles Tavily Ask requests. /search with
// include_answer=true is generally fast (<10s) but advanced search
// depth + many sources can occasionally push past the 30s default.
// 2-minute ceiling reuses core.HTTPClient.Transport for audit.
var longAskClient = &http.Client{
	Timeout:   2 * time.Minute,
	Transport: core.HTTPClient.Transport,
}

type AskProvider struct {
	BaseURL string
	Key     string
}

func NewAsker() *AskProvider { return &AskProvider{BaseURL: defaultAskBase} }

func (*AskProvider) Name() string { return "tavily" }

type askRequest struct {
	APIKey        string `json:"api_key"`
	Query         string `json:"query"`
	SearchDepth   string `json:"search_depth"`
	Topic         string `json:"topic"`
	MaxResults    int    `json:"max_results"`
	IncludeAnswer bool   `json:"include_answer"`
	Days          int    `json:"days,omitempty"`
}

type askResponse struct {
	Answer  string `json:"answer"`
	Results []struct {
		Title         string  `json:"title"`
		URL           string  `json:"url"`
		Content       string  `json:"content"`
		Score         float64 `json:"score"`
		PublishedDate string  `json:"published_date"`
	} `json:"results"`
}

func (p *AskProvider) Ask(ctx context.Context, question string, opts core.AskOptions) (*core.Answer, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("TAVILY_API_KEY")
	}
	if key == "" {
		return nil, errors.New("tavily ask: TAVILY_API_KEY not set")
	}

	body, err := json.Marshal(askRequest{
		APIKey:        key,
		Query:         question,
		SearchDepth:   "advanced",
		Topic:         topicFor(opts.Recency),
		MaxResults:    8,
		IncludeAnswer: true,
		Days:          recencyDays(opts.Recency),
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

	resp, err := longAskClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily ask: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tavily ask: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}
	var data askResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("tavily ask: decode: %w", err)
	}

	sources := make([]core.Source, 0, len(data.Results))
	for _, r := range data.Results {
		s := core.Source{Title: r.Title, URL: r.URL, Snippet: r.Content}
		if t := parseTime(r.PublishedDate); t != nil {
			s.Published = t
		}
		sources = append(sources, s)
	}
	return &core.Answer{
		Question: question,
		Provider: "tavily",
		Text:     strings.TrimSpace(data.Answer),
		Sources:  sources,
		Asked:    time.Now().UTC(),
	}, nil
}

// topicFor: Tavily's `days` filter only fires when topic="news". When
// the user wants recency, switch; otherwise keep general topic.
func topicFor(recency string) string {
	if recency != "" {
		return "news"
	}
	return "general"
}

// recencyDays maps the human shorthand to a day count Tavily accepts.
func recencyDays(r string) int {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "day":
		return 1
	case "week":
		return 7
	case "month":
		return 30
	case "year":
		return 365
	}
	return 0
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
