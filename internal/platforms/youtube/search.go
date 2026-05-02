// Package youtubesearch implements a core.SearchProvider backed by the
// YouTube Data API v3 search.list endpoint.
//
// Auth: YOUTUBE_API_KEY env var (or set Provider.Key explicitly).
// Quota: search.list costs 100 units per call — that's a lot more
// than commentThreads (1) or videos (1). The default 10k/day free
// tier covers ~100 searches; budget-conscious callers should use
// duckduckgo or tavily for broad queries and reach for this one
// only when YouTube-specific signal matters.
package youtube

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

	"github.com/jedi4ever/socialfetch/internal/core"
)

type SearchProvider struct {
	BaseURL string // override for tests
	Key     string // overrides $YOUTUBE_API_KEY when non-empty
	Order   string // search.list ordering: relevance, date, viewCount, rating, title
}

func NewSearchProvider() *SearchProvider {
	return &SearchProvider{
		BaseURL: "https://www.googleapis.com/youtube/v3",
		Order:   "relevance",
	}
}

func (SearchProvider) Name() string { return "youtube" }

// apiResp models the slice of search.list we read.
type apiResp struct {
	Items []struct {
		ID struct {
			VideoID string `json:"videoId"`
		} `json:"id"`
		Snippet struct {
			Title        string `json:"title"`
			Description  string `json:"description"`
			ChannelTitle string `json:"channelTitle"`
			PublishedAt  string `json:"publishedAt"`
		} `json:"snippet"`
	} `json:"items"`
}

func (p *SearchProvider) Search(ctx context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	key := p.Key
	if key == "" {
		key = os.Getenv("YOUTUBE_API_KEY")
	}
	if key == "" {
		return nil, errors.New("youtube search: YOUTUBE_API_KEY not set")
	}
	maxN := opts.Max
	if maxN <= 0 {
		maxN = 10
	}
	// search.list returns at most 50 per page; we don't paginate to
	// keep quota cost predictable (1 search = 100 units regardless of
	// maxResults up to 50).
	if maxN > 50 {
		maxN = 50
	}

	order := p.Order
	if order == "" {
		order = "relevance"
	}
	// When the caller wants recency, the natural mapping is order=date.
	// We still apply the After/Before filters as publishedAfter/Before.
	if opts.After != nil {
		order = "date"
	}

	q := url.Values{
		"part":       {"snippet"},
		"type":       {"video"},
		"q":          {query},
		"maxResults": {strconv.Itoa(maxN)},
		"order":      {order},
		"key":        {key},
	}
	if opts.After != nil {
		q.Set("publishedAfter", opts.After.UTC().Format(time.RFC3339))
	}
	if opts.Before != nil {
		q.Set("publishedBefore", opts.Before.UTC().Format(time.RFC3339))
	}

	endpoint := p.BaseURL + "/search?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("youtube search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("youtube search: HTTP 403 — API key invalid, restricted, or daily quota (10k units, 100/search) exhausted")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("youtube search: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data apiResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("youtube search: decode: %w", err)
	}

	out := make([]core.SearchResult, 0, len(data.Items))
	for _, it := range data.Items {
		if it.ID.VideoID == "" {
			continue
		}
		r := core.SearchResult{
			Title:   strings.TrimSpace(it.Snippet.Title),
			URL:     "https://www.youtube.com/watch?v=" + it.ID.VideoID,
			Snippet: composeSnippet(it.Snippet.ChannelTitle, it.Snippet.Description),
			Source:  "youtube",
		}
		if t, err := time.Parse(time.RFC3339, it.Snippet.PublishedAt); err == nil {
			u := t.UTC()
			r.Published = &u
		}
		out = append(out, r)
	}
	return out, nil
}

// composeSnippet prefixes the channel name to the description so the
// renderer's two-line layout shows "Channel — description excerpt"
// rather than just an orphan description.
func composeSnippet(channel, desc string) string {
	desc = strings.TrimSpace(desc)
	channel = strings.TrimSpace(channel)
	switch {
	case channel == "" && desc == "":
		return ""
	case desc == "":
		return channel
	case channel == "":
		return desc
	default:
		return channel + " — " + desc
	}
}
