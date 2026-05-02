// Package anthropic implements a core.Asker backed by Anthropic's
// Messages API (POST /v1/messages) with the built-in `web_search` server
// tool. Anthropic runs the search loop server-side and returns a single
// response with text content and inline citations.
//
// Auth: ANTHROPIC_API_KEY (https://console.anthropic.com/settings/keys).
// Pricing: $10 per 1,000 searches on top of normal token billing. Each
// search counts as one use regardless of results returned.
//
// Model: defaults to `claude-sonnet-4-6` — cheaper than opus, supports
// the basic `web_search_20250305` tool. Override with `-m
// claude-opus-4-7` for the strongest reasoning, or
// `claude-haiku-4-5-20251001` for the cheapest. Anthropic doesn't expose
// a generic "latest" alias, so the family number does need bumping when
// a new generation ships.
//
// Tool version: we use `web_search_20250305` (basic web search). The
// newer `web_search_20260209` version supports dynamic filtering but
// requires the code execution tool to be enabled — out of scope for a
// single-turn ask.
//
// Headers: Anthropic uses `x-api-key` (not bearer auth) plus a required
// `anthropic-version` header.
//
// Refs:
//   - https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool
//   - https://platform.claude.com/docs/en/api/messages
package anthropic

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

	"github.com/jedi4ever/socialfetch/internal/core"
)

const (
	defaultBase    = "https://api.anthropic.com/v1/messages"
	defaultModel   = "claude-sonnet-4-6"
	defaultVersion = "2023-06-01"
	toolName       = "web_search"
	toolType       = "web_search_20250305"

	// defaultMaxTokens is what we send when opts.MaxTokens is 0.
	// Anthropic's Messages API requires `max_tokens` (no implicit
	// default), so we always send something. 1024 is enough for most
	// grounded answers without runaway billing.
	defaultMaxTokens = 1024
)

// longTimeoutClient: same rationale as OpenAI/Grok. Web-search loops
// regularly exceed the 30s default. Reuses core.HTTPClient.Transport
// for audit instrumentation.
var longTimeoutClient = &http.Client{
	Timeout:   3 * time.Minute,
	Transport: core.HTTPClient.Transport,
}

type Provider struct {
	BaseURL string
	Key     string
	Version string
}

func New() *Provider { return &Provider{BaseURL: defaultBase, Version: defaultVersion} }

func (*Provider) Name() string { return "anthropic" }

// request mirrors the Anthropic Messages API body. `system` is a
// top-level field on Anthropic (unlike OpenAI/perplexity where it's a
// role inside `messages`) — we map opts.Instructions to it directly.
type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []message `json:"messages"`
	System    string    `json:"system,omitempty"`
	Tools     []tool    `json:"tools,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// tool is the shape Anthropic accepts for built-in server tools.
// `max_uses` caps the search-loop iterations so a runaway agent loop
// doesn't blow the budget.
type tool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses,omitempty"`
}

// response models the subset of /v1/messages we read. Real responses
// include id / role / stop_reason / usage which we ignore.
//
// `content` is an array of heterogeneously-typed blocks: `text`
// (final answer pieces), `server_tool_use` (Claude's search query),
// `web_search_tool_result` (raw results). Citations live inline on
// `text` blocks as a `citations` array.
type response struct {
	Model   string         `json:"model"`
	Content []contentBlock `json:"content"`
	Error   *responseError `json:"error,omitempty"`
}

type contentBlock struct {
	Type      string     `json:"type"`
	Text      string     `json:"text,omitempty"`
	Citations []citation `json:"citations,omitempty"`
}

// citation matches Anthropic's `web_search_result_location` envelope.
// `cited_text` is up to 150 chars of the source passage; we store it
// in the Source.Snippet so callers can render it inline.
type citation struct {
	Type      string `json:"type"`
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	CitedText string `json:"cited_text,omitempty"`
}

type responseError struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

func (p *Provider) Ask(ctx context.Context, question string, opts core.AskOptions) (*core.Answer, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	if key == "" {
		return nil, errors.New("anthropic: ANTHROPIC_API_KEY not set")
	}
	model := opts.Model
	if model == "" {
		model = defaultModel
	}
	maxTok := opts.MaxTokens
	if maxTok <= 0 {
		maxTok = defaultMaxTokens
	}
	version := p.Version
	if version == "" {
		version = defaultVersion
	}

	body, err := json.Marshal(request{
		Model:     model,
		MaxTokens: maxTok,
		System:    opts.Instructions,
		Messages: []message{
			{Role: "user", Content: question},
		},
		Tools: []tool{
			{Type: toolType, Name: toolName, MaxUses: 5},
		},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", version)
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := longTimeoutClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("anthropic: HTTP 401 — ANTHROPIC_API_KEY rejected")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("anthropic: HTTP 429 — rate limit: %s", core.HTTPErrorBody(resp))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data response
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("anthropic: decode: %w", err)
	}
	if data.Error != nil && data.Error.Message != "" {
		return nil, fmt.Errorf("anthropic: %s", data.Error.Message)
	}

	answer := extractText(data.Content)
	sources := extractSources(data.Content)

	return &core.Answer{
		Question: question,
		Provider: "anthropic",
		Model:    data.Model,
		Text:     answer,
		Sources:  sources,
		Asked:    time.Now().UTC(),
	}, nil
}

// extractText concatenates every `text`-type content block. Skips
// `server_tool_use` (the search query Claude composed) and
// `web_search_tool_result` (the raw page list) — those are mechanism,
// not answer.
func extractText(blocks []contentBlock) string {
	var b strings.Builder
	for _, c := range blocks {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// extractSources walks every text block's citations[] for url_citation
// entries and dedupes by URL. cited_text becomes the Source.Snippet so
// the markdown renderer can show the supporting passage under each
// numbered source.
func extractSources(blocks []contentBlock) []core.Source {
	var sources []core.Source
	seen := make(map[string]bool)
	for _, c := range blocks {
		if c.Type != "text" {
			continue
		}
		for _, cit := range c.Citations {
			url := strings.TrimSpace(cit.URL)
			if url == "" || seen[url] {
				continue
			}
			seen[url] = true
			sources = append(sources, core.Source{
				Title:   cit.Title,
				URL:     url,
				Snippet: cit.CitedText,
			})
		}
	}
	return sources
}
