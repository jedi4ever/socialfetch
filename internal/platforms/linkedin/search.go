// LinkedIn search via the browser-extension bridge. LinkedIn doesn't
// expose a public search API, so the only path is to drive the user's
// logged-in browser to the content-search results page, scroll to load
// more items, and scrape the cards.
//
// Selector convention: LinkedIn renders search-result posts inside the
// same `feed-shared-update-v2` container it uses on profile activity
// and the home feed, so we reuse extractActivities (the timeline
// extractor) verbatim. If LinkedIn ever splits these layouts, the
// extractor's selector-drift unit test (timeline_extract_test.go)
// surfaces it; a search-specific extractor would be one new file.
package linkedin

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
)

// SearchProvider implements core.SearchProvider for LinkedIn. Bridge
// required (the search results page is gated behind login).
type SearchProvider struct {
	BridgeURL string // overridable; "" → bridge.DefaultEndpoint
}

func NewSearchProvider() *SearchProvider {
	return &SearchProvider{BridgeURL: bridge.DefaultEndpoint}
}

func (SearchProvider) Name() string { return "linkedin" }

// Hard cap on max results. The new LinkedIn search SDUI surfaces
// ~6-10 visible results before requiring an explicit "Load more"
// click that the bridge can't fire today. Setting --max higher than
// what LinkedIn renders just returns fewer items; 50 stays as the
// upper bound we'd hit if/when the bridge adds click support and
// LinkedIn's anti-scraping doesn't get angrier.
const maxLinkedInSearchResults = 50

func (p SearchProvider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("linkedin search: empty query")
	}
	max := opts.Max
	if max <= 0 {
		max = 20
	}
	if max > maxLinkedInSearchResults {
		max = maxLinkedInSearchResults
	}

	audit := core.AuditFromContext(ctx)
	if audit == nil {
		audit = core.NewAuditLogger(nil)
	}

	target := buildSearchURL(query, opts)
	audit.Logf("linkedin search: navigate %s (max=%d)", target, max)

	bridgeURL := p.BridgeURL
	if bridgeURL == "" {
		bridgeURL = bridge.DefaultEndpoint
	}
	client := &bridge.Client{Endpoint: bridgeURL}

	// Same session-lock pattern as LinkedIn timeline — multi-step
	// navigate+scroll-loop must keep the tab pinned to our URL.
	unlock := bridge.SessionLock()
	defer unlock()

	if err := client.Navigate(ctx, target, audit); err != nil {
		return nil, wrapBridgeErr(err)
	}

	acts, err := scrapeWithScroll(ctx, client, target, max, audit)
	if err != nil {
		return nil, wrapBridgeErr(err)
	}

	results := make([]core.SearchResult, 0, len(acts))
	for _, a := range acts {
		// Apply the after/before window when LinkedIn gave us a
		// resolved Published time. Same lenient policy as timeline:
		// undated cards survive (better too much than silently dropped).
		if a.Published != nil {
			if opts.After != nil && a.Published.Before(*opts.After) {
				continue
			}
			if opts.Before != nil && a.Published.After(*opts.Before) {
				continue
			}
		}
		results = append(results, toSearchResult(a))
	}

	// If the legacy feed-shared-update-v2 extractor found nothing,
	// LinkedIn is serving the new server-driven UI (SDUI) for
	// search — hashed CSS classes, no <a> hrefs to individual
	// posts. Fall back to extracting raw post text from the only
	// stable signal that survives: data-testid="expandable-text-box".
	// The results lose per-post URLs (they don't exist in the SDUI
	// HTML at all), but the body text is what the user is after for
	// "what's being said about X" — better than 0 results.
	if len(results) == 0 {
		audit.Logf("linkedin search: legacy extractor empty, trying SDUI fallback")
		fallback, ferr := scrapeSDUIFallback(ctx, client, target, max, audit)
		if ferr == nil && len(fallback) > 0 {
			audit.Logf("linkedin search: SDUI fallback produced %d text snippets", len(fallback))
			results = fallback
		}
	}
	return results, nil
}

// scrapeSDUIFallback extracts results from LinkedIn's new
// server-driven search UI. Strategy:
//
//  1. Sleep ~8s for JS hydration. The initial DOM after navigate is
//     a content-only skeleton with no profile URLs — those get added
//     once React finishes wiring the page. Without the wait, every
//     post would land here with URL=search-page (no per-author link).
//  2. Loop: grab HTML → extract → if not enough yet, scroll →
//     wait briefly → re-extract. LinkedIn search infinite-scrolls
//     ~10 posts at a time; we cap at maxLinkedInSearchResults.
//  3. Pair each post body (data-testid="expandable-text-box") with
//     the LAST profile anchor (`/in/<user>/`) appearing in the
//     byte-stream window between the previous text-box and this
//     one. The actor header renders right above each post body,
//     so the closest preceding profile anchor is the author.
//
// Per-post URLs (`/feed/update/urn:li:activity:.../`) are NOT in the
// rendered HTML even after hydration — LinkedIn wires those via JS
// click handlers. The author profile URL is the best link target we
// can offer without a Voyager-API extension change.
//
// Best-effort degraded mode — when LinkedIn rolls back the DOM or we
// add a richer extraction surface, this fallback can be removed.
func scrapeSDUIFallback(ctx context.Context, client *bridge.Client, target string, max int, audit *core.AuditLogger) ([]core.SearchResult, error) {
	// Wait for JS hydration. 8s is empirically enough on broadband;
	// shorter waits leave the page with text-boxes but no profile
	// anchors hooked up yet.
	select {
	case <-time.After(8 * time.Second):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	const maxRounds = 8
	var results []core.SearchResult
	prevCount := -1
	stalled := 0
	for round := 0; round < maxRounds; round++ {
		got, err := extractSDUIRound(ctx, client, target, audit)
		if err != nil {
			return results, err
		}
		results = got
		if max > 0 && len(results) >= max {
			break
		}
		if len(results) == prevCount {
			stalled++
			if stalled >= 2 {
				break
			}
		} else {
			stalled = 0
		}
		prevCount = len(results)

		// Scroll for the next round and let LinkedIn fetch+render
		// more cards. Same randomization as the legacy timeline
		// loop to look less bot-like.
		if _, err := client.Scroll(ctx, randScrollAmount(), audit); err != nil {
			return results, err
		}
		select {
		case <-time.After(randScrollPause()):
		case <-ctx.Done():
			return results, ctx.Err()
		}
	}
	if max > 0 && len(results) > max {
		results = results[:max]
	}
	return results, nil
}

// extractSDUIRound performs one HTML grab + parse + extract cycle
// for the SDUI page. Pulled out so the scroll loop in
// scrapeSDUIFallback stays readable.
func extractSDUIRound(ctx context.Context, client *bridge.Client, target string, audit *core.AuditLogger) ([]core.SearchResult, error) {
	htmlStr, _, _, err := client.GetTabHTML(ctx, target, audit)
	if err != nil {
		return nil, err
	}

	// Byte-stream extraction (rather than DOM walking) — the new
	// LinkedIn search markup is React-rendered with deeply nested
	// hashed-class divs, and the byte-positions of text-boxes vs
	// profile-anchors are stable signals.
	textBoxIdx := findAllOffsets(htmlStr, `data-testid="expandable-text-box"`)
	profileMatches := profileAnchorRE.FindAllStringSubmatchIndex(htmlStr, -1)
	if len(textBoxIdx) == 0 {
		return nil, nil
	}
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil, fmt.Errorf("linkedin search SDUI: parse: %w", err)
	}
	boxes := findAllByDataTestID(doc, "expandable-text-box")
	if len(boxes) != len(textBoxIdx) {
		// The HTML and the parsed DOM disagree on box count (rare —
		// happens if LinkedIn injects extra text-boxes outside the
		// search results, e.g. in toasts). Fall back to using
		// boxes alone with a generic URL.
		audit.Logf("linkedin search SDUI: dom/html box count mismatch (%d vs %d), URLs may be off", len(boxes), len(textBoxIdx))
	}

	results := make([]core.SearchResult, 0, len(boxes))
	prevBoxOffset := 0
	for i, b := range boxes {
		text := strings.TrimSpace(collapseSpaces(textOf(b)))
		if text == "" {
			continue
		}
		title := firstLine(text, 80)
		snippet := text
		if len(snippet) > 600 {
			snippet = snippet[:600] + "…"
		}
		// Pair with the LAST profile anchor that lies between the
		// previous text-box's offset and this one. The actor header
		// is rendered immediately above the post body, so the closest
		// (last) profile URL before this text-box is the author.
		// Earlier anchors in the same window are tagged-person
		// mentions inside the previous post's body — wrong author.
		thisBoxOffset := -1
		if i < len(textBoxIdx) {
			thisBoxOffset = textBoxIdx[i]
		}
		profileURL := target
		for _, m := range profileMatches {
			anchorStart := m[0]
			if anchorStart > prevBoxOffset && (thisBoxOffset < 0 || anchorStart < thisBoxOffset) {
				// Keep going — we want the last match in the window.
				profileURL = htmlStr[m[2]:m[3]]
			}
			if thisBoxOffset >= 0 && anchorStart >= thisBoxOffset {
				break
			}
		}
		prevBoxOffset = thisBoxOffset
		results = append(results, core.SearchResult{
			Title:   title,
			URL:     profileURL,
			Snippet: snippet,
			Source:  "linkedin",
		})
	}
	return results, nil
}

// profileAnchorRE matches <a href="https://www.linkedin.com/in/<user>/...">
// hrefs. Submatch group 1 is the URL (the value the SearchResult uses).
var profileAnchorRE = regexp.MustCompile(`href="(https://www\.linkedin\.com/in/[^"?]+)"`)

// findAllOffsets returns every byte-offset where needle occurs in s,
// in document order. Cheap stand-in for DOM tree-walking when we
// only need positional information.
func findAllOffsets(s, needle string) []int {
	var out []int
	start := 0
	for {
		i := strings.Index(s[start:], needle)
		if i < 0 {
			return out
		}
		out = append(out, start+i)
		start += i + len(needle)
	}
}

// findAllByDataTestID walks the DOM collecting every node with the
// given data-testid attribute. Local helper so this fallback path
// doesn't reach into the timeline_extract.go private toolkit.
func findAllByDataTestID(n *html.Node, testID string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "data-testid" && a.Val == testID {
					out = append(out, n)
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// buildSearchURL composes the LinkedIn content-search URL. We use
// type=content (posts) — type=people / type=jobs are different result
// shapes and don't fit core.SearchResult cleanly. `origin=` makes the
// LinkedIn UI behave as if the search was driven from the global
// header instead of an organic referral, which loads more cards.
func buildSearchURL(query string, opts core.SearchOptions) string {
	q := url.Values{
		"keywords": {query},
		"origin":   {"GLOBAL_SEARCH_HEADER"},
	}
	// LinkedIn's `datePosted` filter is the closest equivalent to
	// our After bound. Map common windows; fall through silently
	// when After doesn't fit a preset (LinkedIn doesn't accept
	// arbitrary dates here).
	if opts.After != nil {
		if d := time.Since(*opts.After); d > 0 {
			switch {
			case d <= 24*time.Hour:
				q.Set("datePosted", `"past-24h"`)
			case d <= 7*24*time.Hour:
				q.Set("datePosted", `"past-week"`)
			case d <= 30*24*time.Hour:
				q.Set("datePosted", `"past-month"`)
			}
		}
	}
	return "https://www.linkedin.com/search/results/content/?" + q.Encode()
}

// toSearchResult converts a LinkedIn Activity card into a
// core.SearchResult. Title is the first line of the post body capped
// at 80 chars (matches the convention the X / RSS / hackernews
// providers use); Snippet is the body capped at 500 chars.
func toSearchResult(a Activity) core.SearchResult {
	r := core.SearchResult{
		URL:       a.URL,
		Source:    "linkedin",
		Published: a.Published,
	}
	body := strings.TrimSpace(a.Body)
	r.Title = firstLine(body, 80)
	if r.Title == "" {
		r.Title = a.Header
	}
	if len(body) > 500 {
		r.Snippet = body[:500] + "…"
	} else {
		r.Snippet = body
	}
	return r
}

// FindSearchActivities is exported so tests can hand a parsed DOM
// directly. Mirrors extractActivities but keeps a separate name so
// the search-specific test fixture can target this entry point —
// avoids confusion with the timeline extractor when both packages
// add edge cases.
func FindSearchActivities(doc *html.Node, max int) []Activity {
	return extractActivities(doc, max)
}
