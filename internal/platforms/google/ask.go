// Package google implements a core.Asker backed by Google's Gemini
// API with the built-in `google_search` tool — Gemini synthesizes an
// answer grounded in live Google Search results, returning the answer
// plus a `groundingMetadata` block with the supporting URLs.
//
// Auth: GOOGLE_API_KEY (or GEMINI_API_KEY). Same key works as for
// the YouTube Data API; just enable Generative Language API in your
// Google Cloud project. Free tier covers casual use.
//
// Model: optional. We default to `gemini-2.5-flash` — the
// `gemini-flash-latest` and `gemini-pro-latest` aliases now resolve
// to paid-tier models (return HTTP 429 on free-tier keys), so we
// pin to a specific free-tier version. Bump this when Google
// retires the model. Pass `-m <model>` to override (e.g.
// `-m gemini-2.5-pro` for the larger model on a paid key).
//
// Refs:
//
//	https://ai.google.dev/gemini-api/docs/google-search
//	https://ai.google.dev/api/generate-content
package google

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

	"github.com/jedi4ever/social-skills/internal/core"
)

const (
	defaultAskBase  = "https://generativelanguage.googleapis.com/v1beta"
	defaultAskModel = "gemini-2.5-flash" // free-tier eligible; see file header
)

// longAskClient handles Gemini grounding requests, which routinely
// run 30-60s while google_search retrieves and reasons over multiple
// sources. Reuses core.HTTPClient.Transport so audit events still
// emit through the standard transport wrapper.
var longAskClient = &http.Client{
	Timeout:   3 * time.Minute,
	Transport: core.HTTPClient.Transport,
}

type AskProvider struct {
	BaseURL string
	Key     string
}

func NewAsker() *AskProvider { return &AskProvider{BaseURL: defaultAskBase} }

func (*AskProvider) Name() string { return "google" }

type askRequest struct {
	Contents          []askContent `json:"contents"`
	Tools             []askTool    `json:"tools,omitempty"`
	SystemInstruction *askContent  `json:"systemInstruction,omitempty"`
}

type askContent struct {
	Parts []askPart `json:"parts"`
	Role  string    `json:"role,omitempty"`
}

type askPart struct {
	Text string `json:"text"`
}

// tool turns on Gemini's web-grounding tool. The empty struct payload
// is intentional: Google enables the tool by its mere presence in the
// `tools` array.
type askTool struct {
	GoogleSearch struct{} `json:"google_search"`
}

type askResponse struct {
	Candidates []struct {
		Content struct {
			Parts []askPart `json:"parts"`
		} `json:"content"`
		GroundingMetadata struct {
			GroundingChunks []struct {
				Web struct {
					URI   string `json:"uri"`
					Title string `json:"title"`
				} `json:"web,omitempty"`
			} `json:"groundingChunks"`
		} `json:"groundingMetadata"`
	} `json:"candidates"`
}

func (p *AskProvider) Ask(ctx context.Context, question string, opts core.AskOptions) (*core.Answer, error) {
	key := p.Key
	if key == "" {
		for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
			if v := os.Getenv(k); v != "" {
				key = v
				break
			}
		}
	}
	if key == "" {
		return nil, errors.New("google ask: GOOGLE_API_KEY (or GEMINI_API_KEY) not set")
	}

	model := opts.Model
	if model == "" {
		model = defaultAskModel
	}

	reqBody := askRequest{
		Contents: []askContent{
			{Role: "user", Parts: []askPart{{Text: question}}},
		},
		Tools: []askTool{{}}, // empty struct — google_search tool enabled
	}
	if opts.Instructions != "" {
		// Gemini's systemInstruction field takes the same shape as a
		// content message; only `parts` is consulted (role is ignored
		// for system instructions per the API spec).
		reqBody.SystemInstruction = &askContent{
			Parts: []askPart{{Text: opts.Instructions}},
		}
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.BaseURL, model, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := longAskClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google ask: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("google ask: HTTP 403 — key invalid, restricted, or Generative Language API not enabled: %s", core.HTTPErrorBody(resp))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("google ask: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data askResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("google ask: decode: %w", err)
	}
	if len(data.Candidates) == 0 {
		return nil, fmt.Errorf("google ask: no candidates returned")
	}
	cand := data.Candidates[0]

	var b strings.Builder
	for _, p := range cand.Content.Parts {
		b.WriteString(p.Text)
	}
	answer := strings.TrimSpace(b.String())

	sources := make([]core.Source, 0, len(cand.GroundingMetadata.GroundingChunks))
	for _, c := range cand.GroundingMetadata.GroundingChunks {
		if c.Web.URI == "" {
			continue
		}
		sources = append(sources, core.Source{
			Title: c.Web.Title,
			URL:   c.Web.URI,
		})
	}

	return &core.Answer{
		Question: question,
		Provider: "google",
		Model:    model,
		Text:     answer,
		Sources:  sources,
		Asked:    time.Now().UTC(),
	}, nil
}
