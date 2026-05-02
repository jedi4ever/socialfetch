// Package serpapi's Ask side adapts SerpAPI's google_ai_overview
// engine as an answer engine. Returns Google's AI-synthesized answer
// card with reference links — handy for "tell me what Google's AI
// thinks" kinds of questions.
//
// Auth: same SERPAPI_KEY as the regular SerpAPI search provider.
//
// Two-step flow (current as of mid-2026):
//
//  1. Discovery: call engine=google with the query. The response has
//     an `ai_overview` block iff Google generated an overview. That
//     block carries either inline `text_blocks` (short overviews) or
//     just a `page_token` (longer overviews require a second fetch).
//  2. Expand: when only a page_token is present, call
//     engine=google_ai_overview&page_token=... to retrieve the full
//     overview content.
//
// Caveat: not every query triggers an AI Overview. We surface that as
// a clear error so callers can retry on a different question or fall
// back to a regular search.
//
// Limitations:
//   - opts.Model is ignored — Google picks the AI Overview model.
//   - opts.Instructions is ignored — google_ai_overview takes no
//     system-prompt parameter.
//   - opts.Recency is ignored — overview content is what Google
//     decides to surface for the query as-is.
package serpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultAskBase = "https://serpapi.com/search.json"

type AskProvider struct {
	BaseURL string
	Key     string
}

func NewAsker() *AskProvider { return &AskProvider{BaseURL: defaultAskBase} }

func (*AskProvider) Name() string { return "serpapi" }

// aiOverview is the shared shape returned by both /search?engine=google
// (in the `ai_overview` field of the larger response) and
// /search?engine=google_ai_overview (as the top-level `ai_overview`).
type aiOverview struct {
	PageToken  string        `json:"page_token,omitempty"`
	Snippet    string        `json:"snippet,omitempty"`
	TextBlocks []aiTextBlock `json:"text_blocks,omitempty"`
	References []aiReference `json:"references,omitempty"`
}

type aiTextBlock struct {
	Type    string        `json:"type"`
	Snippet string        `json:"snippet,omitempty"`
	List    []aiTextBlock `json:"list,omitempty"` // nested for type=list
	Title   string        `json:"title,omitempty"`
}

type aiReference struct {
	Title  string `json:"title"`
	Link   string `json:"link"`
	Source string `json:"source,omitempty"`
}

// discoveryResponse is what /search?engine=google returns when an AI
// Overview is present.
type discoveryResponse struct {
	AIOverview *aiOverview `json:"ai_overview,omitempty"`
}

// expandResponse is what /search?engine=google_ai_overview returns
// for the second-step expansion call.
type expandResponse struct {
	AIOverview *aiOverview `json:"ai_overview,omitempty"`
}

func (p *AskProvider) Ask(ctx context.Context, question string, opts core.AskOptions) (*core.Answer, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("SERPAPI_KEY")
	}
	if key == "" {
		return nil, errors.New("serpapi ask: SERPAPI_KEY not set")
	}

	// Step 1: discovery. Call regular Google search; the response
	// includes an `ai_overview` block iff Google generated one.
	disc, err := p.discover(ctx, key, question)
	if err != nil {
		return nil, err
	}
	if disc == nil {
		return nil, fmt.Errorf("serpapi ask: no AI Overview returned for %q (Google didn't generate one for this query)", question)
	}

	// Step 2: expand if only a page_token came back. Some overviews
	// inline their text_blocks already; for those we skip the second
	// call.
	overview := disc
	if disc.PageToken != "" && len(disc.TextBlocks) == 0 && disc.Snippet == "" {
		overview, err = p.expand(ctx, key, disc.PageToken)
		if err != nil {
			return nil, err
		}
	}

	answer := flattenOverview(overview)
	sources := make([]core.Source, 0, len(overview.References))
	for _, r := range overview.References {
		title := r.Title
		if title == "" {
			title = r.Source
		}
		sources = append(sources, core.Source{Title: title, URL: r.Link})
	}

	if answer == "" && len(sources) == 0 {
		return nil, fmt.Errorf("serpapi ask: no AI Overview content for %q after expansion", question)
	}

	return &core.Answer{
		Question: question,
		Provider: "serpapi",
		Text:     answer,
		Sources:  sources,
		Asked:    time.Now().UTC(),
	}, nil
}

// discover runs the engine=google call and returns the embedded
// ai_overview block. Returns (nil, nil) when Google didn't generate
// an overview for the query.
func (p *AskProvider) discover(ctx context.Context, key, question string) (*aiOverview, error) {
	q := url.Values{
		"engine":  {"google"},
		"q":       {question},
		"api_key": {key},
		"hl":      {"en"},
	}
	body, err := p.do(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("serpapi ask: discovery: %w", err)
	}
	var data discoveryResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("serpapi ask: discovery decode: %w", err)
	}
	return data.AIOverview, nil
}

// expand fetches the full overview content for a page_token returned
// by the discovery step.
func (p *AskProvider) expand(ctx context.Context, key, pageToken string) (*aiOverview, error) {
	q := url.Values{
		"engine":     {"google_ai_overview"},
		"page_token": {pageToken},
		"api_key":    {key},
	}
	body, err := p.do(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("serpapi ask: expand: %w", err)
	}
	var data expandResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("serpapi ask: expand decode: %w", err)
	}
	if data.AIOverview == nil {
		return nil, fmt.Errorf("serpapi ask: expand returned no ai_overview")
	}
	return data.AIOverview, nil
}

// do issues a single GET to SerpAPI and returns the response body
// bytes, surfacing any non-2xx via core.HTTPErrorBody.
func (p *AskProvider) do(ctx context.Context, q url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", core.UserAgent)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}
	return io.ReadAll(resp.Body)
}

// flattenOverview turns the structured ai_overview text_blocks (with
// nested lists for bullet points) into a single readable markdown
// string. Falls back to the legacy `snippet` field when no blocks
// are present.
func flattenOverview(o *aiOverview) string {
	if s := strings.TrimSpace(o.Snippet); s != "" && len(o.TextBlocks) == 0 {
		return s
	}
	var b strings.Builder
	for _, tb := range o.TextBlocks {
		switch tb.Type {
		case "paragraph", "":
			if s := strings.TrimSpace(tb.Snippet); s != "" {
				b.WriteString(s)
				b.WriteString("\n\n")
			}
		case "heading":
			if s := strings.TrimSpace(tb.Snippet); s != "" {
				b.WriteString("## " + s + "\n\n")
			}
		case "list":
			for _, item := range tb.List {
				if t := strings.TrimSpace(item.Title); t != "" {
					b.WriteString("- **" + t + "**")
					if s := strings.TrimSpace(item.Snippet); s != "" {
						b.WriteString(": " + s)
					}
					b.WriteByte('\n')
				} else if s := strings.TrimSpace(item.Snippet); s != "" {
					b.WriteString("- " + s + "\n")
				}
			}
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}
