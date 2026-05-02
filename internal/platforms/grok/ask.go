// Package grok implements a core.Asker backed by xAI's Grok models
// with web grounding via the Agent Tools API on the /v1/responses
// endpoint. xAI deprecated the older Live-Search-on-/v1/chat/completions
// path in 2026; the new path uses the OpenAI-Responses-compatible
// schema (model + input array + tools array) and returns a top-level
// `citations` URL list plus structured `output[]` messages.
//
// Auth: XAI_API_KEY (or GROK_API_KEY). Sign up at
// https://console.x.ai. Tool use is billed as token usage plus a
// per-tool-invocation fee — see https://docs.x.ai/developers/models.
//
// Model: optional. We omit `model` from the request body unless the
// caller passes one via opts.Model — xAI then picks an account-level
// default (currently grok-4-0709). This avoids the chore of bumping
// a hardcoded "latest" string every time xAI ships a new flagship.
// Pass `-m grok-4.3` (or whichever version you want) to override.
//
// Refs:
//   - https://docs.x.ai/docs/guides/tools/overview
//   - https://docs.x.ai/developers/tools/web-search
//   - https://docs.x.ai/developers/tools/citations
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

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultBase = "https://api.x.ai/v1/responses"

// longTimeoutClient is used for Ask() requests because xAI's
// Agent-Tools loop can spend 30-90s browsing sources before returning.
// Reuses core.HTTPClient.Transport so audit events still emit.
var longTimeoutClient = &http.Client{
	Timeout:   3 * time.Minute,
	Transport: core.HTTPClient.Transport,
}

type Provider struct {
	BaseURL string
	Key     string
}

func New() *Provider { return &Provider{BaseURL: defaultBase} }

func (*Provider) Name() string { return "grok" }

// request mirrors xAI's /v1/responses body. Only the fields we set
// are present here; the API has many more (top_logprobs, truncation,
// service_tier, etc.) which all default sensibly.
//
// Model is omitempty so an empty opts.Model leaves the field out of
// the body entirely — xAI then picks an account default. Same for
// Instructions: only present when the caller asked for it.
type request struct {
	Model           string         `json:"model,omitempty"`
	Input           []inputMessage `json:"input"`
	Instructions    string         `json:"instructions,omitempty"`
	Tools           []tool         `json:"tools,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// tool is the JSON shape xAI accepts for built-in tools. Only
// `web_search` is wired in here; future capabilities (x_search,
// code_interpreter, collections_search) plug in at the same level.
type tool struct {
	Type    string            `json:"type"`
	Filters *webSearchFilters `json:"filters,omitempty"`
}

type webSearchFilters struct {
	AllowedDomains  []string `json:"allowed_domains,omitempty"`
	ExcludedDomains []string `json:"excluded_domains,omitempty"`
}

// response models the subset of /v1/responses we read. The full
// schema includes id / status / created_at / usage / etc. — we
// intentionally ignore those.
//
// `output` is an array of items; for our purposes only `type=message`
// items matter, and each carries a `content` array of text blocks.
// `citations` is a flat list of URLs the agent visited during the
// query — always returned by default.
type response struct {
	Model     string         `json:"model"`
	Status    string         `json:"status"`
	Output    []outputItem   `json:"output"`
	Citations []string       `json:"citations,omitempty"`
	Error     *responseError `json:"error,omitempty"`
}

type outputItem struct {
	Type    string         `json:"type"`
	Role    string         `json:"role,omitempty"`
	Content []contentBlock `json:"content,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responseError struct {
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
}

func (p *Provider) Ask(ctx context.Context, question string, opts core.AskOptions) (*core.Answer, error) {
	key := p.Key
	if key == "" {
		key = firstEnv("XAI_API_KEY", "GROK_API_KEY")
	}
	if key == "" {
		return nil, errors.New("grok: XAI_API_KEY not set")
	}
	body, err := json.Marshal(request{
		Model: opts.Model, // omitempty drops it when empty so xAI picks
		Input: []inputMessage{
			{Role: "user", Content: question},
		},
		Instructions: opts.Instructions,
		Tools: []tool{
			{Type: "web_search"},
		},
		MaxOutputTokens: opts.MaxTokens,
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

	// xAI's Agent Tools loop (web_search → browse → reason) regularly
	// exceeds the default 30s HTTPClient timeout. Use a longer-timeout
	// client that reuses the audit-instrumented transport so events
	// still land in the global audit log.
	resp, err := longTimeoutClient.Do(req)
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
		return nil, fmt.Errorf("grok: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data response
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("grok: decode: %w", err)
	}
	if data.Error != nil && data.Error.Message != "" {
		return nil, fmt.Errorf("grok: %s", data.Error.Message)
	}

	answer := extractText(data.Output)

	sources := make([]core.Source, 0, len(data.Citations))
	for _, u := range data.Citations {
		if strings.TrimSpace(u) != "" {
			sources = append(sources, core.Source{URL: u})
		}
	}

	return &core.Answer{
		Question: question,
		Provider: "grok",
		Model:    data.Model,
		Text:     answer,
		Sources:  sources,
		Asked:    time.Now().UTC(),
	}, nil
}

// extractText walks every text block in every message-typed output
// item and returns the concatenation. The Responses API can interleave
// reasoning blocks, tool-call traces, and final text; we only surface
// the text the user is meant to read.
func extractText(items []outputItem) string {
	var b strings.Builder
	for _, it := range items {
		if it.Type != "message" && it.Type != "" {
			continue
		}
		for _, c := range it.Content {
			if c.Type == "output_text" || c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
