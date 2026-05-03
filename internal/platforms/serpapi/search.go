// Package serpapi implements a core.SearchProvider backed by SerpAPI
// (https://serpapi.com). Requires a SERPAPI_KEY environment variable
// (or an explicit Key on the Provider).
//
// Two registered variants:
//
//   - "serpapi"      — Google web search (default)
//   - "serpapi-news" — Google News (tbm=nws), better for time-
//     sensitive queries since results are dated and
//     ranked by recency rather than authority.
//
// Both share the same key and pagination logic; only the search type
// differs.
//
// Locale / geo can be tuned with SERPAPI_GL (country code, e.g. "us"
// or "fr"), SERPAPI_HL (interface language, e.g. "en"), and
// SERPAPI_LOCATION (city, e.g. "Austin, TX"). These persist across
// calls; per-call overrides aren't exposed since most agents pick
// once and stick with it.
//
// We model only the JSON fields we use; the response has many more.
package serpapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jedi4ever/social-skills/internal/core"
)

// pageSize is SerpAPI's default page width — Google's organic_results
// caps at ~10 per request, with a hard ceiling of 100 via num=100.
// We page in 10s when auto-paginating so charges scale linearly with
// requested-results-count instead of always-100-charged.
const pageSize = 10

// maxPages caps auto-pagination so a misconfigured Max=10000 doesn't
// silently rack up SerpAPI charges. 5 pages × 10/page = 50 results,
// which is plenty for "give me a real answer" queries; agents that
// need more can call again with an explicit Start offset.
const maxPages = 5

func applyDomainOpsSerp(query string, opts core.SearchOptions) string {
	parts := []string{query}
	for _, d := range opts.IncludeDomains {
		parts = append(parts, "site:"+d)
	}
	for _, d := range opts.ExcludeDomains {
		parts = append(parts, "-site:"+d)
	}
	return strings.Join(parts, " ")
}

func serpDateRange(opts core.SearchOptions) string {
	if opts.After == nil && opts.Before == nil {
		return ""
	}
	min := ""
	max := ""
	if opts.After != nil {
		min = opts.After.UTC().Format("01/02/2006")
	}
	if opts.Before != nil {
		max = opts.Before.UTC().Format("01/02/2006")
	}
	parts := []string{"cdr:1"}
	if min != "" {
		parts = append(parts, "cd_min:"+min)
	}
	if max != "" {
		parts = append(parts, "cd_max:"+max)
	}
	return strings.Join(parts, ",")
}

// Provider queries SerpAPI's google engine. SearchType picks between
// the regular web SERP ("" / "web") and the News-tab SERP ("news",
// emitted as tbm=nws). Engine stays as "google" for both.
type Provider struct {
	BaseURL    string
	Key        string
	Engine     string // defaults to "google"
	SearchType string // "" / "web" → web SERP; "news" → tbm=nws
	GL         string // country code (e.g. "us"); falls back to SERPAPI_GL
	HL         string // interface language (e.g. "en"); falls back to SERPAPI_HL
	Location   string // city-level location; falls back to SERPAPI_LOCATION
}

// New returns the regular web-search Provider. Locale / geo fields
// are left empty; they pick up SERPAPI_GL / SERPAPI_HL /
// SERPAPI_LOCATION at call time so the registry doesn't need to
// re-create the provider when env vars change.
func New() *Provider {
	return &Provider{
		BaseURL: "https://serpapi.com/search.json",
		Engine:  "google",
	}
}

func (p Provider) Name() string {
	if p.SearchType == "news" {
		return "serpapi-news"
	}
	return "serpapi"
}

type response struct {
	OrganicResults []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
		Date    string `json:"date,omitempty"`
	} `json:"organic_results"`
	NewsResults []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
		Date    string `json:"date,omitempty"`
		Source  string `json:"source,omitempty"`
	} `json:"news_results"`
	Error string `json:"error"`
}

func (p *Provider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("SERPAPI_KEY")
	}
	if key == "" {
		return nil, errors.New("serpapi: SERPAPI_KEY not set; pass --search-key or set the env var")
	}
	needed := opts.Max
	if needed <= 0 {
		needed = 10
	}

	engine := p.Engine
	if engine == "" {
		engine = "google"
	}

	// Resolve geo / locale from explicit Provider fields first, then
	// the SERPAPI_* env vars. Keeps the call site free of per-request
	// plumbing — set once per process.
	gl := firstNonEmpty(p.GL, os.Getenv("SERPAPI_GL"))
	hl := firstNonEmpty(p.HL, os.Getenv("SERPAPI_HL"))
	loc := firstNonEmpty(p.Location, os.Getenv("SERPAPI_LOCATION"))

	tbs := serpDateRange(opts)
	queryStr := applyDomainOpsSerp(query, opts)

	// Auto-paginate. Each loop iteration is one charged SerpAPI
	// request; we keep going until we have `needed` results, the
	// upstream returns a short page (=end of results), or we hit
	// maxPages.
	//
	// We always request a full `pageSize` per call rather than
	// shrinking to remaining-needed: the "got fewer than I asked
	// for → no more results upstream" heuristic only works when
	// we ask for a fixed-size page each time. Trimming to
	// remaining=3 on the last call would falsely look like "the
	// upstream ran out" even when there were more pages available.
	// The final slice trims overfetch back to `needed`.
	results := make([]core.SearchResult, 0, needed)
	start := opts.Start
	for page := 0; page < maxPages && len(results) < needed; page++ {
		q := url.Values{
			"q":       {queryStr},
			"engine":  {engine},
			"api_key": {key},
			"num":     {fmt.Sprint(pageSize)},
		}
		if start > 0 {
			q.Set("start", fmt.Sprint(start))
		}
		if tbs != "" {
			q.Set("tbs", tbs)
		}
		if p.SearchType == "news" {
			q.Set("tbm", "nws")
		}
		if gl != "" {
			q.Set("gl", gl)
		}
		if hl != "" {
			q.Set("hl", hl)
		}
		if loc != "" {
			q.Set("location", loc)
		}

		endpoint := p.BaseURL + "?" + q.Encode()
		var resp response
		if err := core.GetJSON(ctx, endpoint, &resp); err != nil {
			return nil, fmt.Errorf("serpapi: %w", err)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("serpapi: %s", resp.Error)
		}

		// Normalize the per-page payload from whichever block the
		// SERP type returned. News mode populates news_results; web
		// mode populates organic_results.
		hits := normalizeResults(resp, p.SearchType)
		if len(hits) == 0 {
			break
		}
		results = append(results, hits...)
		start += len(hits)
		// Short-fill = end of result set. If Google returned fewer
		// than a full pageSize, there's nothing left upstream — keep
		// looping would just charge for empty pages.
		if len(hits) < pageSize {
			break
		}
	}

	if len(results) > needed {
		results = results[:needed]
	}
	return results, nil
}

// normalizeResults turns a SerpAPI response into the source-agnostic
// SearchResult shape. News-mode normalization lives in news.go since
// it has its own snippet shape (date + source name prepended).
func normalizeResults(r response, searchType string) []core.SearchResult {
	if searchType == "news" {
		return normalizeNewsResults(r)
	}
	out := make([]core.SearchResult, 0, len(r.OrganicResults))
	for _, hit := range r.OrganicResults {
		out = append(out, core.SearchResult{
			Title:   hit.Title,
			URL:     hit.Link,
			Snippet: hit.Snippet,
			Source:  "serpapi",
		})
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
