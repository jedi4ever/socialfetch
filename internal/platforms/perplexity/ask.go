// Package perplexity implements a core.Asker backed by Perplexity's
// "Sonar" online models. The API speaks the OpenAI Chat Completions
// shape; the synthesized answer comes back in choices[0].message
// .content and the cited URLs in `citations` (older shape) or
// `search_results` (newer shape).
//
// Auth: PERPLEXITY_API_KEY (or PPLX_API_KEY). Sign up at
// https://www.perplexity.ai/settings/api.
//
// Model: optional. Defaults to `sonar` (Perplexity's auto-tracking
// alias for their cheapest grounded variant). Override with --model:
//
//	sonar           cheapest grounded model (default)
//	sonar-pro       larger context, better synthesis
//	sonar-reasoning reasoning model with web search
//
// Instructions are passed through as a `system`-role message
// prepended to the messages array — standard Chat Completions idiom.
package perplexity

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

const defaultBase = "https://api.perplexity.ai/chat/completions"
const defaultModel = "sonar"

// longAskClient handles Perplexity Sonar requests, which can run
// 30-90s when the model fans out to multiple sources. Reuses
// core.HTTPClient.Transport so audit events still emit.
var longAskClient = &http.Client{
	Timeout:   3 * time.Minute,
	Transport: core.HTTPClient.Transport,
}

type Provider struct {
	BaseURL string
	Key     string
}

func New() *Provider { return &Provider{BaseURL: defaultBase} }

func (*Provider) Name() string { return "perplexity" }

type request struct {
	Model    string    `json:"model"`
	Messages []chatMsg `json:"messages"`
	// SearchRecencyFilter narrows the corpus Perplexity searches.
	SearchRecencyFilter string `json:"search_recency_filter,omitempty"`
	MaxTokens           int    `json:"max_tokens,omitempty"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type response struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	// Older shape: array of URL strings.
	Citations []string `json:"citations,omitempty"`
	// Newer shape: array of objects with title/url/date.
	SearchResults []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Date  string `json:"date,omitempty"`
	} `json:"search_results,omitempty"`
}

func (p *Provider) Ask(ctx context.Context, question string, opts core.AskOptions) (*core.Answer, error) {
	key := p.Key
	if key == "" {
		key = firstEnv("PERPLEXITY_API_KEY", "PPLX_API_KEY")
	}
	if key == "" {
		return nil, errors.New("perplexity: PERPLEXITY_API_KEY not set")
	}
	model := opts.Model
	if model == "" {
		model = defaultModel
	}

	// Build messages: optional system instruction, then the user's
	// question. Perplexity's Chat Completions endpoint accepts the
	// standard role=system message for persistent guidance.
	msgs := make([]chatMsg, 0, 2)
	if opts.Instructions != "" {
		msgs = append(msgs, chatMsg{Role: "system", Content: opts.Instructions})
	}
	msgs = append(msgs, chatMsg{Role: "user", Content: question})

	body, err := json.Marshal(request{
		Model:               model,
		Messages:            msgs,
		SearchRecencyFilter: opts.Recency,
		MaxTokens:           opts.MaxTokens,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := longAskClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perplexity: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("perplexity: HTTP 401 — PERPLEXITY_API_KEY rejected: %s", core.HTTPErrorBody(resp))
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("perplexity: HTTP 429 — rate limit hit: %s", core.HTTPErrorBody(resp))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("perplexity: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data response
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("perplexity: decode: %w", err)
	}
	answer := ""
	if len(data.Choices) > 0 {
		answer = strings.TrimSpace(data.Choices[0].Message.Content)
	}

	// Prefer the newer `search_results` shape (titled, with dates) and
	// fall back to the legacy `citations` URL list.
	sources := make([]core.Source, 0, len(data.SearchResults)+len(data.Citations))
	for _, r := range data.SearchResults {
		s := core.Source{Title: r.Title, URL: r.URL}
		if t := parseTime(r.Date); t != nil {
			s.Published = t
		}
		sources = append(sources, s)
	}
	if len(sources) == 0 {
		for _, u := range data.Citations {
			if strings.TrimSpace(u) != "" {
				sources = append(sources, core.Source{URL: u})
			}
		}
	}

	return &core.Answer{
		Question: question,
		Provider: "perplexity",
		Model:    data.Model,
		Text:     answer,
		Sources:  sources,
		Asked:    time.Now().UTC(),
	}, nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
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
