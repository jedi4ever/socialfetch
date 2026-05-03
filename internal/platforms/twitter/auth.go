// Package xauth implements X (Twitter) OAuth 2.0 App-Only — the read-only
// flow that exchanges a consumer key+secret for an app bearer token. The
// X v2 API requires this for any endpoint that doesn't act on behalf of a
// user.
//
// Both the X search provider and the official-API path of the Twitter
// fetcher use this. We cache the bearer token in-process for the lifetime
// of the binary because it doesn't expire (until revoked).
package twitter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/jedi4ever/social-skills/internal/core"
)

// TokenURL is the OAuth2 token endpoint. Exposed as a var so tests can
// point it at a fake server.
var TokenURL = "https://api.twitter.com/oauth2/token"

// Credentials are the consumer key and secret from an X developer app.
// Read these from the environment via FromEnv.
type Credentials struct {
	Key    string
	Secret string
}

// FromEnv returns credentials from $X_API_KEY and $X_API_SECRET. Returns
// (zero, false) when either is missing — callers can use this to decide
// whether to fall back to an unauthenticated path.
func FromEnv() (Credentials, bool) {
	k, s := os.Getenv("X_API_KEY"), os.Getenv("X_API_SECRET")
	if k == "" || s == "" {
		return Credentials{}, false
	}
	return Credentials{Key: k, Secret: s}, true
}

var (
	cacheMu sync.Mutex
	cache   = map[string]string{} // key+secret hash -> bearer token
)

// BearerToken returns an app bearer token, fetching one if needed. The
// token is cached in-process; subsequent calls with the same credentials
// are free.
func BearerToken(ctx context.Context, c Credentials) (string, error) {
	if c.Key == "" || c.Secret == "" {
		return "", errors.New("xauth: X_API_KEY/X_API_SECRET not set")
	}
	cacheKey := c.Key + ":" + c.Secret

	cacheMu.Lock()
	if tok, ok := cache[cacheKey]; ok {
		cacheMu.Unlock()
		return tok, nil
	}
	cacheMu.Unlock()

	tok, err := fetchToken(ctx, c)
	if err != nil {
		return "", err
	}

	cacheMu.Lock()
	cache[cacheKey] = tok
	cacheMu.Unlock()
	return tok, nil
}

// fetchToken performs the actual OAuth2 client-credentials request.
func fetchToken(ctx context.Context, c Credentials) (string, error) {
	creds := base64.StdEncoding.EncodeToString([]byte(
		urlEnc(c.Key) + ":" + urlEnc(c.Secret),
	))

	body := strings.NewReader("grant_type=client_credentials")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic "+creds)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("xauth: token endpoint returned HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}
	var out struct {
		TokenType   string `json:"token_type"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("xauth: decode token: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("xauth: empty access_token in response")
	}
	return out.AccessToken, nil
}

// urlEnc applies the percent-encoding X expects on the consumer key/secret
// before base64 — matching net/url's QueryEscape but only over the
// reserved set X actually requires.
func urlEnc(s string) string {
	return url.QueryEscape(s)
}

// ResetCache clears the in-process token cache. Useful in tests to make
// sure each scenario triggers a fresh token exchange.
func ResetCache() {
	cacheMu.Lock()
	cache = map[string]string{}
	cacheMu.Unlock()
}
