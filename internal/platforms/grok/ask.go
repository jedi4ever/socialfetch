// Package grok implements an ask.Asker backed by xAI's Grok models
// with Live Search enabled. The API speaks the OpenAI Chat Completions
// shape. We turn on web grounding by including a search_parameters
// block in the request body — without it, Grok answers from its
// training data only.
//
// Auth: XAI_API_KEY (or GROK_API_KEY). Sign up at
// https://console.x.ai. Live Search costs a small per-source fee on
// top of token usage.
//
// Default model: grok-4-fast — fastest grounded variant. Override:
//
//	grok-3              standard chat
//	grok-4-fast         fast, web-grounded
//	grok-4              full Grok 4 with reasoning
package grok

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

	"github.com/patrickdebois/social-skills/internal/ask"
	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultBase = "https://api.x.ai/v1/chat/completions"
const defaultModel = "grok-4-fast"

type Provider struct {
	BaseURL string
	Key     string
}

func New() *Provider { return &Provider{BaseURL: defaultBase} }

func (*Provider) Name() string { return "grok" }

type request struct {
	Model            string             `json:"model"`
	Messages         []chatMsg          `json:"messages"`
	MaxTokens        int                `json:"max_tokens,omitempty"`
	SearchParameters *searchParameters  `json:"search_parameters,omitempty"`
}

type searchParameters struct {
	// Mode "auto" lets Grok decide whether to search; "on" forces it.
	// We always force it for the ask subcommand — the whole point is
	// grounded answers.
	Mode    string `json:"mode"`
	FromDate string `json:"from_date,omitempty"` // YYYY-MM-DD
	ToDate   string `json:"to_date,omitempty"`
	// ReturnCitations controls whether the response includes a
	// `citations` array. Always on for our use.
	ReturnCitations bool `json:"return_citations"`
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
	// xAI returns citations as a flat URL list at the top level of
	// the response when search_parameters.return_citations=true.
	Citations []string `json:"citations,omitempty"`
}

func (p *Provider) Ask(ctx context.Context, question string, opts ask.Options) (*ask.Answer, error) {
	key := p.Key
	if key == "" {
		key = firstEnv("XAI_API_KEY", "GROK_API_KEY")
	}
	if key == "" {
		return nil, errors.New("grok: XAI_API_KEY not set")
	}
	model := opts.Model
	if model == "" {
		model = defaultModel
	}

	sp := &searchParameters{Mode: "on", ReturnCitations: true}
	if opts.Recency != "" {
		sp.FromDate = recencyToFromDate(opts.Recency)
	}

	body, err := json.Marshal(request{
		Model: model,
		Messages: []chatMsg{
			{Role: "user", Content: question},
		},
		MaxTokens:        opts.MaxTokens,
		SearchParameters: sp,
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

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grok: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("grok: HTTP 401 — XAI_API_KEY rejected")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("grok: HTTP 429 — rate limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("grok: HTTP %d", resp.StatusCode)
	}

	var data response
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("grok: decode: %w", err)
	}

	answer := ""
	if len(data.Choices) > 0 {
		answer = strings.TrimSpace(data.Choices[0].Message.Content)
	}
	sources := make([]ask.Source, 0, len(data.Citations))
	for _, u := range data.Citations {
		if strings.TrimSpace(u) != "" {
			sources = append(sources, ask.Source{URL: u})
		}
	}

	return &ask.Answer{
		Question: question,
		Provider: "grok",
		Model:    data.Model,
		Text:     answer,
		Sources:  sources,
		Asked:    time.Now().UTC(),
	}, nil
}

// recencyToFromDate maps "day"/"week"/"month"/"year" to an absolute
// from_date string. Anything else is passed through verbatim (so users
// can supply a YYYY-MM-DD literal directly).
func recencyToFromDate(r string) string {
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "day":
		return now.AddDate(0, 0, -1).Format("2006-01-02")
	case "week":
		return now.AddDate(0, 0, -7).Format("2006-01-02")
	case "month":
		return now.AddDate(0, -1, 0).Format("2006-01-02")
	case "year":
		return now.AddDate(-1, 0, 0).Format("2006-01-02")
	}
	return r
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
