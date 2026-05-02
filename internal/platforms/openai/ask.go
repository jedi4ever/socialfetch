// Package openai implements a core.Asker backed by OpenAI's Responses
// API (POST /v1/responses) with the built-in `web_search` tool. The
// schema is the OpenAI-canonical version of what xAI/grok mirrors —
// model + input + tools — but citations come back differently: they
// live on each text block as `annotations` with `type=url_citation`,
// not a flat top-level array.
//
// Auth: OPENAI_API_KEY (https://platform.openai.com/api-keys). Tool
// calls are billed as token usage plus a per-call fee for hosted tools
// like web_search. See https://platform.openai.com/docs/guides/tools-web-search.
//
// Model: defaults to `gpt-5.5` (current recommended for new
// Responses-API integrations). Unlike xAI, OpenAI's Responses API
// requires `model` — there's no account-default fallback at the API
// level — so we have to send something. Override with `-m
// gpt-5.5-mini` (cheaper) or any other GPT-4-tier-or-later model. Web
// search isn't supported on 3.5-tier models.
//
// Refs:
//   - https://developers.openai.com/api/docs/guides/tools-web-search
//   - https://platform.openai.com/docs/api-reference/responses
package openai

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

const (
	defaultBase  = "https://api.openai.com/v1/responses"
	defaultModel = "gpt-5.5"
)

// longTimeoutClient is used for Ask() requests because the
// Responses-API agent loop (web_search → fetch → reason) regularly
// runs 30-90s on multi-source questions. Reuses core.HTTPClient.Transport
// so audit events still emit.
var longTimeoutClient = &http.Client{
	Timeout:   3 * time.Minute,
	Transport: core.HTTPClient.Transport,
}

type Provider struct {
	BaseURL string
	Key     string
}

func New() *Provider { return &Provider{BaseURL: defaultBase} }

func (*Provider) Name() string { return "openai" }

// request mirrors OpenAI's /v1/responses body. Only the fields we set
// are present — the API has many more (top_logprobs, truncation,
// service_tier, etc.) which all default sensibly.
//
// Model is required by the API (no `omitempty`) — we always fall back
// to defaultModel when opts.Model is empty.
type request struct {
	Model           string         `json:"model"`
	Input           []inputMessage `json:"input"`
	Instructions    string         `json:"instructions,omitempty"`
	Tools           []tool         `json:"tools,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// tool is the JSON shape the Responses API accepts for hosted tools.
// Only `web_search` is wired here; future capabilities (file_search,
// code_interpreter, computer_use_preview, ...) plug in at the same
// level.
type tool struct {
	Type string `json:"type"`
}

// response models the subset of /v1/responses we read. The full schema
// includes id / status / created_at / usage / etc. — we intentionally
// ignore those.
//
// `output` is an array of items; for our purposes only `type=message`
// items matter, and each carries a `content` array of text blocks.
// Unlike xAI, OpenAI does NOT return a top-level `citations` array —
// citations are embedded in `content[].annotations` with
// `type=url_citation`. We walk those out into core.Source entries.
type response struct {
	Model  string         `json:"model"`
	Status string         `json:"status"`
	Output []outputItem   `json:"output"`
	Error  *responseError `json:"error,omitempty"`
}

type outputItem struct {
	Type    string         `json:"type"`
	Role    string         `json:"role,omitempty"`
	Content []contentBlock `json:"content,omitempty"`
}

type contentBlock struct {
	Type        string       `json:"type"`
	Text        string       `json:"text,omitempty"`
	Annotations []annotation `json:"annotations,omitempty"`
}

// annotation is OpenAI's citation envelope. Only `url_citation` is
// surfaced today; `file_citation` and `file_path` exist for other
// tools. `start_index`/`end_index` mark the byte offsets in `text`
// the citation backs — useful for inline rendering, ignored here.
type annotation struct {
	Type       string `json:"type"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
	StartIndex int    `json:"start_index,omitempty"`
	EndIndex   int    `json:"end_index,omitempty"`
}

type responseError struct {
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

func (p *Provider) Ask(ctx context.Context, question string, opts core.AskOptions) (*core.Answer, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key == "" {
		return nil, errors.New("openai: OPENAI_API_KEY not set")
	}
	model := opts.Model
	if model == "" {
		model = defaultModel
	}
	body, err := json.Marshal(request{
		Model: model,
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

	resp, err := longTimeoutClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("openai: HTTP 401 — OPENAI_API_KEY rejected")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("openai: HTTP 429 — rate limit: %s", core.HTTPErrorBody(resp))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data response
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("openai: decode: %w", err)
	}
	if data.Error != nil && data.Error.Message != "" {
		return nil, fmt.Errorf("openai: %s", data.Error.Message)
	}

	answer := extractText(data.Output)
	sources := extractSources(data.Output)

	return &core.Answer{
		Question: question,
		Provider: "openai",
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

// extractSources collects every `url_citation` annotation from every
// text block, deduping by URL so a heavily-cited source isn't listed
// five times.
func extractSources(items []outputItem) []core.Source {
	var sources []core.Source
	seen := make(map[string]bool)
	for _, it := range items {
		if it.Type != "message" && it.Type != "" {
			continue
		}
		for _, c := range it.Content {
			for _, a := range c.Annotations {
				if a.Type != "url_citation" {
					continue
				}
				url := strings.TrimSpace(a.URL)
				if url == "" || seen[url] {
					continue
				}
				seen[url] = true
				sources = append(sources, core.Source{
					Title: a.Title,
					URL:   url,
				})
			}
		}
	}
	return sources
}
