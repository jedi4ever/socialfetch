// Package xsearch implements a search.Provider backed by X's recent
// tweets search v2 endpoint (last 7 days, free-tier-friendly).
//
// Auth: OAuth 2.0 App-Only — exchanges X_API_KEY+X_API_SECRET for an app
// bearer token via xauth.BearerToken.
//
// Query syntax is X v2 search: -is:retweet, lang:en, from:user, etc.
package xsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"
	"github.com/patrickdebois/social-skills/internal/xauth"
)

// Provider queries the X v2 recent search endpoint.
type Provider struct {
	BaseURL string
	// Creds optionally overrides $X_API_KEY/$X_API_SECRET. Mostly used by
	// tests; production code leaves it zero so FromEnv is consulted.
	Creds xauth.Credentials
}

func New() *Provider {
	return &Provider{BaseURL: "https://api.twitter.com/2/tweets/search/recent"}
}

func (Provider) Name() string { return "x" }

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

func (p *Provider) Search(ctx context.Context, query string, max int) ([]search.Result, error) {
	creds := p.Creds
	if creds.Key == "" || creds.Secret == "" {
		c, ok := xauth.FromEnv()
		if !ok {
			return nil, errors.New("x search: X_API_KEY / X_API_SECRET not set")
		}
		creds = c
	}
	bearer, err := xauth.BearerToken(ctx, creds)
	if err != nil {
		return nil, err
	}

	// X recent-search bounds max_results between 10 and 100.
	switch {
	case max <= 0:
		max = 10
	case max < 10:
		max = 10
	case max > 100:
		max = 100
	}

	q := url.Values{
		"query":        {query},
		"max_results":  {fmt.Sprint(max)},
		"expansions":   {"author_id"},
		"tweet.fields": {"created_at,public_metrics,lang,note_tweet"},
		"user.fields":  {"username,name"},
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
		return nil, fmt.Errorf("x search: HTTP %d", resp.StatusCode)
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
		out = append(out, search.Result{
			Title:   "@" + title,
			URL:     tweetURL,
			Snippet: text,
			Source:  "x",
		})
	}
	return out, nil
}
