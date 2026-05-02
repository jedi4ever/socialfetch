// Package redditsearch implements a core.SearchProvider backed by Reddit's
// public search.json endpoint — no auth required for read-only queries,
// though Reddit rate-limits anonymous traffic aggressively (~60 req/min
// per IP) and is strict about User-Agent strings. core.UserAgent is
// already a real-browser-ish identifier, which is enough.
//
// Endpoint: https://www.reddit.com/search.json?q=QUERY&limit=N&sort=...
//
// When opts.IncludeDomains is set we scope the search to those subs by
// switching to /r/<sub>/search.json with restrict_sr=on. Multiple subs
// are unioned by OR-joining `subreddit:` operators in the query, since
// Reddit's URL-form only takes one subreddit at a time.
//
// Reddit's coarse time window (t=hour|day|week|month|year|all) is
// translated from opts.After: anything within the last hour/day/...
// picks the matching window; finer-grained windows fall back to "all"
// and we filter client-side on created_utc.
package reddit

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

// Provider queries the public Reddit search endpoint.
type SearchProvider struct {
	// BaseURL is the search endpoint root. Tests override to point at
	// an httptest server.
	BaseURL string
	// Sort defaults to "relevance"; other valid values: "hot", "top",
	// "new", "comments". Mirrors Reddit's UI.
	Sort string
	// Restrict, when non-empty, forces a single-subreddit search via
	// /r/<sub>/search.json — overrides any subreddit operator in the
	// query string.
	Restrict string
}

func NewSearchProvider() *SearchProvider {
	return &SearchProvider{
		BaseURL: "https://www.reddit.com",
		Sort:    "relevance",
	}
}

func (SearchProvider) Name() string { return "reddit" }

// listing mirrors Reddit's pagination envelope. We decode only the
// fields we surface — extras are ignored.
type searchListing struct {
	Data struct {
		Children []struct {
			Data struct {
				Title       string  `json:"title"`
				Permalink   string  `json:"permalink"`
				URL         string  `json:"url"`
				Subreddit   string  `json:"subreddit"`
				Author      string  `json:"author"`
				Selftext    string  `json:"selftext"`
				Score       int     `json:"score"`
				NumComments int     `json:"num_comments"`
				CreatedUTC  float64 `json:"created_utc"`
				Over18      bool    `json:"over_18"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

func (p *SearchProvider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	max := opts.Max
	if max <= 0 {
		max = 10
	}
	if max > 100 {
		max = 100 // Reddit caps limit at 100
	}

	q := applyOperators(query, opts)
	values := url.Values{
		"q":     {q},
		"limit": {fmt.Sprint(max)},
		"sort":  {p.Sort},
	}
	if t := redditTimeBucket(opts.After); t != "" {
		values.Set("t", t)
	}

	endpoint := p.BaseURL + "/search.json"
	if p.Restrict != "" {
		endpoint = p.BaseURL + "/r/" + p.Restrict + "/search.json"
		values.Set("restrict_sr", "on")
	}
	full := endpoint + "?" + values.Encode()

	var resp searchListing
	if err := core.GetJSON(ctx, full, &resp); err != nil {
		return nil, fmt.Errorf("reddit search: %w", err)
	}

	results := make([]core.SearchResult, 0, len(resp.Data.Children))
	for _, child := range resp.Data.Children {
		d := child.Data

		// Client-side date filter for windows finer than Reddit's coarse
		// `t=...` buckets. created_utc is seconds since epoch.
		if d.CreatedUTC > 0 {
			ts := time.Unix(int64(d.CreatedUTC), 0)
			if opts.After != nil && ts.Before(*opts.After) {
				continue
			}
			if opts.Before != nil && ts.After(*opts.Before) {
				continue
			}
		}

		permalink := d.Permalink
		if permalink != "" && !strings.HasPrefix(permalink, "http") {
			permalink = "https://www.reddit.com" + permalink
		}
		// For link posts, surface the linked URL; for self-posts, the
		// permalink IS the canonical URL.
		linkURL := d.URL
		if linkURL == "" || strings.Contains(linkURL, "reddit.com/r/") {
			linkURL = permalink
		}
		title := fmt.Sprintf("%s (r/%s, %d points, %d comments)",
			d.Title, d.Subreddit, d.Score, d.NumComments)
		snippet := strings.TrimSpace(d.Selftext)
		if snippet == "" {
			snippet = "by u/" + d.Author
		}
		var published *time.Time
		if d.CreatedUTC > 0 {
			t := time.Unix(int64(d.CreatedUTC), 0).UTC()
			published = &t
		}

		results = append(results, core.SearchResult{
			Title:     title,
			URL:       linkURL,
			Snippet:   snippet,
			Source:    "reddit",
			Published: published,
		})
	}
	return results, nil
}

// applyOperators folds include/exclude domains and subreddit hints
// into the query string using Reddit's native search operators.
//
//   - IncludeDomains -> `(site:a.com OR site:b.com)`
//   - ExcludeDomains -> `-site:c.com`
//   - The plain query is preserved as-is.
//
// Reddit's search does support `site:` and `subreddit:` operators on
// its full-text search backend.
func applyOperators(query string, opts core.SearchOptions) string {
	parts := []string{strings.TrimSpace(query)}
	if len(opts.IncludeDomains) == 1 {
		parts = append(parts, "site:"+opts.IncludeDomains[0])
	} else if len(opts.IncludeDomains) > 1 {
		var or []string
		for _, d := range opts.IncludeDomains {
			or = append(or, "site:"+d)
		}
		parts = append(parts, "("+strings.Join(or, " OR ")+")")
	}
	for _, d := range opts.ExcludeDomains {
		parts = append(parts, "-site:"+d)
	}
	return strings.Join(parts, " ")
}

// redditTimeBucket maps an opts.After time to the closest Reddit `t`
// bucket — Reddit only natively supports hour/day/week/month/year/all.
// Values closer than the bucket boundary are upgraded to the next finer
// bucket (e.g. 23 hours -> day, not hour) so we err on the side of
// returning more results, then the caller filters precisely on
// created_utc.
func redditTimeBucket(after *time.Time) string {
	if after == nil {
		return ""
	}
	age := time.Since(*after)
	switch {
	case age <= time.Hour:
		return "hour"
	case age <= 24*time.Hour:
		return "day"
	case age <= 7*24*time.Hour:
		return "week"
	case age <= 31*24*time.Hour:
		return "month"
	case age <= 366*24*time.Hour:
		return "year"
	default:
		return "all"
	}
}
