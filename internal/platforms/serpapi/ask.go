// Package serpapiask adapts SerpAPI's google_ai_overview engine as an
// answer engine. Returns Google's AI-synthesized answer card with
// reference links — handy for "tell me what Google's AI thinks" kinds
// of questions.
//
// Auth: same SERPAPI_KEY as the regular SerpAPI search provider.
//
// Caveat: not every query triggers an AI Overview on Google; when no
// overview exists, SerpAPI returns an empty payload. We surface that
// as an empty Answer + a clear note in the audit log so callers can
// retry on a different question or fall back to a regular search.
package serpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/ask"
	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultAskBase = "https://serpapi.com/search.json"

type AskProvider struct {
	BaseURL string
	Key     string
}

func NewAsker() *AskProvider { return &AskProvider{BaseURL: defaultAskBase} }

func (*AskProvider) Name() string { return "serpapi" }

type askResponse struct {
	AIOverview struct {
		// SerpAPI returns either text_blocks (newer) or a single
		// `ai_overview.snippet` (legacy). We read both.
		TextBlocks []struct {
			Type    string `json:"type"`
			Snippet string `json:"snippet,omitempty"`
		} `json:"text_blocks,omitempty"`
		Snippet    string `json:"snippet,omitempty"`
		References []struct {
			Title  string `json:"title"`
			Link   string `json:"link"`
			Source string `json:"source,omitempty"`
		} `json:"references,omitempty"`
	} `json:"ai_overview"`
}

func (p *AskProvider) Ask(ctx context.Context, question string, opts ask.Options) (*ask.Answer, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("SERPAPI_KEY")
	}
	if key == "" {
		return nil, errors.New("serpapi ask: SERPAPI_KEY not set")
	}

	q := url.Values{
		"engine": {"google_ai_overview"},
		"q":      {question},
		"api_key": {key},
		"hl":      {"en"},
	}
	endpoint := p.BaseURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("serpapi ask: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("serpapi ask: HTTP %d", resp.StatusCode)
	}

	var data askResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("serpapi ask: decode: %w", err)
	}

	answer := strings.TrimSpace(data.AIOverview.Snippet)
	if answer == "" {
		// Concatenate text_blocks of type "paragraph" / "list".
		var b strings.Builder
		for _, tb := range data.AIOverview.TextBlocks {
			if s := strings.TrimSpace(tb.Snippet); s != "" {
				b.WriteString(s)
				b.WriteByte('\n')
			}
		}
		answer = strings.TrimSpace(b.String())
	}

	sources := make([]ask.Source, 0, len(data.AIOverview.References))
	for _, r := range data.AIOverview.References {
		title := r.Title
		if title == "" {
			title = r.Source
		}
		sources = append(sources, ask.Source{Title: title, URL: r.Link})
	}

	if answer == "" && len(sources) == 0 {
		return nil, fmt.Errorf("serpapi ask: no AI Overview returned for %q (Google didn't generate one for this query)", question)
	}

	return &ask.Answer{
		Question: question,
		Provider: "serpapi",
		Text:     answer,
		Sources:  sources,
		Asked:    time.Now().UTC(),
	}, nil
}
