// Package google implements a core.SearchProvider backed by Google's
// Custom Search JSON API. Free tier: 100 q/day (then $5 per 1k).
//
// Setup:
//  1. Get GOOGLE_API_KEY from https://console.cloud.google.com → enable
//     "Custom Search API" → Credentials → Create API key.
//  2. Create a CSE at https://programmablesearchengine.google.com →
//     configure to "Search the entire web" → copy the engine ID.
//  3. Set GOOGLE_API_KEY + GOOGLE_CSE_ID in your shell or .env.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultBase = "https://www.googleapis.com/customsearch/v1"

type Provider struct {
	BaseURL string
	Key     string
	CSEID   string
}

func New() *Provider { return &Provider{BaseURL: defaultBase} }

func (*Provider) Name() string { return "google" }

type apiResp struct {
	Items []struct {
		Title       string `json:"title"`
		Link        string `json:"link"`
		Snippet     string `json:"snippet"`
		DisplayLink string `json:"displayLink,omitempty"`
		Pagemap     struct {
			Metatags []map[string]string `json:"metatags,omitempty"`
		} `json:"pagemap,omitempty"`
	} `json:"items"`
}

func (p *Provider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("GOOGLE_API_KEY")
	}
	cse := p.CSEID
	if cse == "" {
		cse = os.Getenv("GOOGLE_CSE_ID")
	}
	if key == "" {
		return nil, errors.New("google search: GOOGLE_API_KEY not set")
	}
	if cse == "" {
		return nil, errors.New("google search: GOOGLE_CSE_ID not set (create one at https://programmablesearchengine.google.com)")
	}

	maxN := opts.Max
	if maxN <= 0 {
		maxN = 10
	}
	// Custom Search caps `num` at 10 per call. We don't paginate to
	// keep quota cost predictable.
	if maxN > 10 {
		maxN = 10
	}

	q := url.Values{
		"q":   {query},
		"key": {key},
		"cx":  {cse},
		"num": {strconv.Itoa(maxN)},
	}
	// `dateRestrict` is the API's recency knob: d[N], w[N], m[N], y[N].
	if opts.After != nil {
		q.Set("dateRestrict", dateRestrictFor(*opts.After))
	}
	endpoint := p.BaseURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("google search: HTTP 403 — key invalid, Custom Search API not enabled, or quota exhausted (100/day free)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("google search: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}
	var data apiResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("google search: decode: %w", err)
	}

	out := make([]core.SearchResult, 0, len(data.Items))
	for _, it := range data.Items {
		r := core.SearchResult{
			Title:   strings.TrimSpace(it.Title),
			URL:     it.Link,
			Snippet: it.Snippet,
			Source:  "google",
		}
		// Some Google CSE responses expose article:published_time in
		// the metatags array; capture it when present.
		for _, mt := range it.Pagemap.Metatags {
			for _, k := range []string{"article:published_time", "og:published_time", "datePublished"} {
				if v := mt[k]; v != "" {
					if t := parseTime(v); t != nil {
						r.Published = t
					}
					break
				}
			}
			if r.Published != nil {
				break
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// dateRestrictFor maps a relative window to the API's d[N]/w[N]/m[N]/y[N]
// shorthand. We use the smallest unit that covers the window.
func dateRestrictFor(after time.Time) string {
	hours := time.Since(after).Hours()
	switch {
	case hours <= 36:
		return "d1"
	case hours <= 7*24:
		return fmt.Sprintf("d%d", int(hours/24)+1)
	case hours <= 31*24:
		return fmt.Sprintf("w%d", int(hours/(7*24))+1)
	case hours <= 366*24:
		return fmt.Sprintf("m%d", int(hours/(30*24))+1)
	}
	return fmt.Sprintf("y%d", int(hours/(365*24))+1)
}

func parseTime(s string) *time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
