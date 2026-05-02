// Package xsearch implements a search.Provider backed by X's recent
// tweets search v2 endpoint (last 7 days, free-tier-friendly).
//
// Auth: OAuth 2.0 App-Only — exchanges X_API_KEY+X_API_SECRET for an app
// bearer token via BearerToken.
//
// Query syntax is X v2 search: -is:retweet, lang:en, from:user, etc.
package twitter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"
)

// XSearchMaxAgeDays is X's recent-search window. The free/basic tier of
// the v2 endpoint rejects start_time older than 7 days with HTTP 400.
const XSearchMaxAgeDays = 7

// applyXOperators turns search.Options into X v2 search operators.
// X uses `from:user`, `-from:user`, `domain:host`, `-domain:host`.
// We use `domain:` for domain filters since site: isn't a thing on X.
func applyXOperators(query string, opts search.Options) string {
	parts := []string{query}
	for _, d := range opts.IncludeDomains {
		parts = append(parts, "domain:"+d)
	}
	for _, d := range opts.ExcludeDomains {
		parts = append(parts, "-domain:"+d)
	}
	return strings.Join(parts, " ")
}

// Provider queries the X v2 recent search endpoint.
type SearchProvider struct {
	BaseURL string
	// Creds optionally overrides $X_API_KEY/$X_API_SECRET. Mostly used by
	// tests; production code leaves it zero so FromEnv is consulted.
	Creds Credentials
}

func NewSearchProvider() *SearchProvider {
	return &SearchProvider{BaseURL: "https://api.twitter.com/2/tweets/search/recent"}
}

func (SearchProvider) Name() string { return "x" }

type response struct {
	Data []struct {
		ID            string `json:"id"`
		Text          string `json:"text"`
		AuthorID      string `json:"author_id"`
		CreatedAt     string `json:"created_at"`
		PublicMetrics struct {
			Likes    int `json:"like_count"`
			Reposts  int `json:"retweet_count"`
			Replies  int `json:"reply_count"`
		} `json:"public_metrics"`
		NoteTweet *struct {
			Text string `json:"text"`
		} `json:"note_tweet"`
	} `json:"data"`
	Includes struct {
		Users []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"users"`
	} `json:"includes"`
	Errors []struct {
		Message string `json:"message"`
		Title   string `json:"title"`
	} `json:"errors"`
}

func (p *SearchProvider) Search(ctx context.Context, query string, opts search.Options) ([]search.Result, error) {
	creds := p.Creds
	if creds.Key == "" || creds.Secret == "" {
		c, ok := FromEnv()
		if !ok {
			return nil, errors.New("x search: X_API_KEY / X_API_SECRET not set")
		}
		creds = c
	}
	bearer, err := BearerToken(ctx, creds)
	if err != nil {
		return nil, err
	}

	max := opts.Max
	// X recent-search bounds max_results between 10 and 100.
	switch {
	case max <= 0:
		max = 10
	case max < 10:
		max = 10
	case max > 100:
		max = 100
	}

	// X v2 recent-search supports inline operators; map portable
	// Options into them. Date bounds use start_time/end_time as
	// dedicated query params (more precise than the from:/until:
	// inline operators which are date-granular).
	q := url.Values{
		"query":        {applyXOperators(query, opts)},
		"max_results":  {fmt.Sprint(max)},
		"expansions":   {"author_id"},
		"tweet.fields": {"created_at,public_metrics,lang,note_tweet"},
		"user.fields":  {"username,name"},
	}
	if opts.After != nil {
		// Allow a small slack: the After time is computed by the CLI
		// slightly before this check runs, so an exact "7 days ago"
		// from the user appears a few microseconds older here. Without
		// slack, --last 7d intermittently rejects.
		cutoff := time.Now().UTC().Add(-XSearchMaxAgeDays*24*time.Hour - time.Minute)
		if opts.After.Before(cutoff) {
			return nil, fmt.Errorf("x search: --after/--last must be within the last %d days (X v2 recent-search tier limit); got %s",
				XSearchMaxAgeDays, opts.After.UTC().Format("2006-01-02"))
		}
		// X also requires start_time to be at least 10s before the
		// request — clamp to be safe.
		earliest := time.Now().UTC().Add(-XSearchMaxAgeDays * 24 * time.Hour).Add(time.Minute)
		start := opts.After.UTC()
		if start.Before(earliest) {
			start = earliest
		}
		q.Set("start_time", start.Format("2006-01-02T15:04:05Z"))
	}
	if opts.Before != nil {
		q.Set("end_time", opts.Before.UTC().Format("2006-01-02T15:04:05Z"))
	}
	endpoint := p.BaseURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("x search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("x search: HTTP %d: %s", resp.StatusCode, decodeXError(raw))
	}

	var body response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("x search: decode: %w", err)
	}
	if len(body.Errors) > 0 {
		return nil, fmt.Errorf("x search: %s", body.Errors[0].Message)
	}

	users := map[string]string{} // id -> username
	for _, u := range body.Includes.Users {
		users[u.ID] = u.Username
	}

	out := make([]search.Result, 0, len(body.Data))
	for _, t := range body.Data {
		text := t.Text
		if t.NoteTweet != nil && t.NoteTweet.Text != "" {
			// Long-form posts (>280 chars) live in note_tweet.text.
			text = t.NoteTweet.Text
		}
		username := users[t.AuthorID]
		tweetURL := fmt.Sprintf("https://x.com/%s/status/%s", username, t.ID)
		title := username
		if title == "" {
			title = t.ID
		}
		var published *time.Time
		if t.CreatedAt != "" {
			if pt, err := time.Parse(time.RFC3339, t.CreatedAt); err == nil {
				pt = pt.UTC()
				published = &pt
			}
		}
		out = append(out, search.Result{
			Title:     "@" + title,
			URL:       tweetURL,
			Snippet:   text,
			Source:    "x",
			Published: published,
		})
	}
	return out, nil
}

// decodeXError extracts a useful message from an X API error response.
// X returns either {errors:[{message}]} or {title,detail} depending on
// the error class. Falls back to a trimmed snippet of the raw body so we
// never swallow context.
func decodeXError(raw []byte) string {
	if len(raw) == 0 {
		return "(empty body)"
	}
	var withErrors struct {
		Errors []struct {
			Message string `json:"message"`
			Title   string `json:"title"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &withErrors); err == nil && len(withErrors.Errors) > 0 {
		e := withErrors.Errors[0]
		if e.Message != "" {
			return e.Message
		}
		if e.Title != "" {
			return e.Title
		}
	}
	var problem struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &problem); err == nil {
		switch {
		case problem.Detail != "" && problem.Title != "":
			return problem.Title + ": " + problem.Detail
		case problem.Detail != "":
			return problem.Detail
		case problem.Title != "":
			return problem.Title
		}
	}
	snippet := strings.TrimSpace(string(raw))
	if len(snippet) > 200 {
		snippet = snippet[:200] + "…"
	}
	return snippet
}
