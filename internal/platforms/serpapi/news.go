package serpapi

import "github.com/jedi4ever/social-skills/internal/core"

// SerpAPI news search — Google's News tab via tbm=nws. Sister to the
// regular web search in search.go; shares everything (auth, paging,
// geo) except the SERP type and the response field name (news_results
// instead of organic_results).
//
// Why this is its own file: the news SERP differs enough at the
// JSON-shape boundary (extra Date/Source fields per hit, a snippet
// that prefixes the source name) that grouping the variant logic
// here keeps search.go focused on the common-case web search path.
// The two share Provider so callers see one type with a SearchType
// field, and registration in cmd/social-fetch picks the right one.

// NewNewsProvider returns a SerpAPI provider that queries the Google
// News tab (tbm=nws). Same key, same auto-pagination, same date /
// site filters — different SERP. Registered alongside the regular
// web provider so agents can pick `-p serpapi-news` for time-
// sensitive queries without changing their flag set.
//
// The Provider's Name() method returns "serpapi-news" for this
// variant; SearchResult.Source carries the same value through to
// callers.
func NewNewsProvider() *Provider {
	p := New()
	p.SearchType = "news"
	return p
}

// normalizeNewsResults converts the news_results block of a SerpAPI
// response into SearchResult. The snippet gets a "<date> · <source>
// — <body>" prefix so the agent can read freshness + outlet right
// from the result list without a follow-up fetch — important for
// news-mode where "is this from today or 2018?" is the first
// question the LLM should be able to answer.
func normalizeNewsResults(r response) []core.SearchResult {
	out := make([]core.SearchResult, 0, len(r.NewsResults))
	for _, n := range r.NewsResults {
		snippet := n.Snippet
		if n.Source != "" {
			if snippet != "" {
				snippet = n.Source + " — " + snippet
			} else {
				snippet = n.Source
			}
		}
		if n.Date != "" {
			snippet = n.Date + " · " + snippet
		}
		out = append(out, core.SearchResult{
			Title:   n.Title,
			URL:     n.Link,
			Snippet: snippet,
			Source:  "serpapi-news",
		})
	}
	return out
}
