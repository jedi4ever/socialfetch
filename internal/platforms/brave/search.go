// Package bravesearch implements a core.SearchProvider backed by Brave
// Search (https://api.search.brave.com). Brave is a generally-strong
// alternative to DuckDuckGo for tech queries — privacy-focused, doesn't
// rely on Bing or Google for ranking, and exposes a clean JSON API.
//
// Auth: BRAVE_API_KEY env var (or set Provider.Key). Free tier is
// 2,000 queries/month; sign up at https://api.search.brave.com.
package brave

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

type Provider struct {
	BaseURL string // override for tests
	Key     string // overrides $BRAVE_API_KEY
}

func New() *Provider {
	return &Provider{BaseURL: "https://api.search.brave.com/res/v1/web/search"}
}

func (Provider) Name() string { return "brave" }

type apiResp struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			PageAge     string `json:"page_age,omitempty"` // RFC3339 when present
			Age         string `json:"age,omitempty"`      // human-readable, fallback
		} `json:"results"`
	} `json:"web"`
}

func (p *Provider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("BRAVE_API_KEY")
	}
	if key == "" {
		return nil, errors.New("brave search: BRAVE_API_KEY not set")
	}
	maxN := opts.Max
	if maxN <= 0 {
		maxN = 10
	}
	// Brave caps `count` at 20 per call.
	if maxN > 20 {
		maxN = 20
	}

	q := url.Values{
		"q":     {query},
		"count": {strconv.Itoa(maxN)},
	}
	// Brave exposes recency via the `freshness` parameter (pd=day,
	// pw=week, pm=month, py=year, or YYYY-MM-DDtoYYYY-MM-DD ranges).
	// Map opts.After to the closest preset; for arbitrary windows we
	// build the explicit range form.
	if opts.After != nil {
		q.Set("freshness", freshnessFor(*opts.After, opts.Before))
	}

	endpoint := p.BaseURL + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", key)
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("brave search: HTTP 401 — BRAVE_API_KEY rejected")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("brave search: HTTP 429 — rate limit hit (free tier: ~1 query/sec, 2k/month)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("brave search: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data apiResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("brave search: decode: %w", err)
	}
	out := make([]core.SearchResult, 0, len(data.Web.Results))
	for _, r := range data.Web.Results {
		res := core.SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
			Source:  "brave",
		}
		if t := parseTime(r.PageAge); t != nil {
			res.Published = t
		}
		// Defensive post-filter using publication dates we *do* have.
		if res.Published != nil {
			if opts.After != nil && res.Published.Before(*opts.After) {
				continue
			}
			if opts.Before != nil && res.Published.After(*opts.Before) {
				continue
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// freshnessFor maps a date window to Brave's freshness parameter. We
// prefer the explicit range form over presets so --last 7d / --after
// work precisely; presets are a fallback when only opts.After is set
// and it lines up with day/week/month.
func freshnessFor(after time.Time, before *time.Time) string {
	if before == nil {
		now := time.Now().UTC()
		days := int(now.Sub(after).Hours()/24) + 1
		switch {
		case days <= 1:
			return "pd"
		case days <= 7:
			return "pw"
		case days <= 31:
			return "pm"
		case days <= 365:
			return "py"
		}
		// Fall through to explicit range.
	}
	end := time.Now().UTC()
	if before != nil {
		end = before.UTC()
	}
	return after.UTC().Format("2006-01-02") + "to" + end.Format("2006-01-02")
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
