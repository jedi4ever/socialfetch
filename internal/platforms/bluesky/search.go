// Package bluesky implements a search.Provider backed by Bluesky's
// app.bsky.feed.searchPosts XRPC method.
//
// Auth: Bluesky now requires an authenticated session for searchPosts
// (the public AppView returns 403). We do a com.atproto.server.create
// Session call with BLUESKY_HANDLE + BLUESKY_APP_PASSWORD and use the
// returned accessJwt as a bearer token. The post fetcher (sources/
// bluesky) doesn't need auth since getPostThread is still public.
//
// Generate an app password at https://bsky.app/settings/app-passwords
// — never use your account password.
package bluesky

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"
)

const (
	// publicBase serves no-auth read endpoints (resolveHandle, getPostThread).
	publicBase = "https://public.api.bsky.app/xrpc"
	// authBase serves session-bearing endpoints (createSession, searchPosts).
	authBase = "https://bsky.social/xrpc"
)

type SearchProvider struct {
	// PublicBase / AuthBase override the base URLs for tests.
	PublicBase string
	AuthBase   string

	// Handle / AppPassword override $BLUESKY_HANDLE / $BLUESKY_APP_PASSWORD.
	Handle      string
	AppPassword string

	mu       sync.Mutex
	sessJWT  string
	sessTime time.Time
}

func NewSearchProvider() *SearchProvider {
	return &SearchProvider{PublicBase: publicBase, AuthBase: authBase}
}

func (*SearchProvider) Name() string { return "bluesky" }

type apiResp struct {
	Cursor string `json:"cursor"`
	Posts  []struct {
		URI    string `json:"uri"`
		Author struct {
			Handle      string `json:"handle"`
			DisplayName string `json:"displayName"`
		} `json:"author"`
		Record struct {
			Text      string `json:"text"`
			CreatedAt string `json:"createdAt"`
		} `json:"record"`
		LikeCount  int `json:"likeCount"`
		ReplyCount int `json:"replyCount"`
	} `json:"posts"`
}

func (p *SearchProvider) Search(ctx context.Context, query string, opts search.Options) ([]search.Result, error) {
	jwt, err := p.session(ctx)
	if err != nil {
		return nil, err
	}

	maxN := opts.Max
	if maxN <= 0 {
		maxN = 25
	}
	// Bluesky caps limit at 100; clamp.
	if maxN > 100 {
		maxN = 100
	}

	q := url.Values{
		"q":     {query},
		"limit": {strconv.Itoa(maxN)},
	}
	// Native date filter: `since` (RFC3339) and `until`. Honor both.
	if opts.After != nil {
		q.Set("since", opts.After.UTC().Format(time.RFC3339))
	}
	if opts.Before != nil {
		q.Set("until", opts.Before.UTC().Format(time.RFC3339))
	}

	endpoint := p.authBaseURL() + "/app.bsky.feed.searchPosts?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("User-Agent", core.UserAgent)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bluesky search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		// Token may have expired; clear and let the next call refresh.
		p.mu.Lock()
		p.sessJWT = ""
		p.mu.Unlock()
		return nil, fmt.Errorf("bluesky search: HTTP 401 — session token rejected (try again)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bluesky search: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}
	var data apiResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("bluesky search: decode: %w", err)
	}

	out := make([]search.Result, 0, len(data.Posts))
	for _, p := range data.Posts {
		// AT URI looks like at://did:plc:abc/app.bsky.feed.post/rkey
		// — convert to the public web URL bsky.app uses.
		webURL := atURIToWebURL(p.URI, p.Author.Handle)
		if webURL == "" {
			continue
		}
		r := search.Result{
			Title:   firstLine(p.Record.Text, 100),
			URL:     webURL,
			Snippet: p.Record.Text,
			Source:  "bluesky",
		}
		if t, err := time.Parse(time.RFC3339Nano, p.Record.CreatedAt); err == nil {
			u := t.UTC()
			r.Published = &u
		} else if t, err := time.Parse(time.RFC3339, p.Record.CreatedAt); err == nil {
			u := t.UTC()
			r.Published = &u
		}
		out = append(out, r)
	}
	return out, nil
}

// atURIToWebURL converts an at:// URI plus the post author's handle
// into the bsky.app web URL. The rkey is the last path segment.
func atURIToWebURL(uri, handle string) string {
	// at://did/app.bsky.feed.post/<rkey>
	const prefix = "at://"
	if len(uri) <= len(prefix) || uri[:len(prefix)] != prefix {
		return ""
	}
	// Last segment after slash is the rkey.
	rest := uri[len(prefix):]
	last := -1
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i] == '/' {
			last = i
			break
		}
	}
	if last < 0 || last == len(rest)-1 {
		return ""
	}
	rkey := rest[last+1:]
	if handle == "" {
		return ""
	}
	return "https://bsky.app/profile/" + handle + "/post/" + rkey
}


// session returns a cached accessJwt or runs createSession to mint
// one. The token is reused across calls in this process; if Bluesky
// rejects it (401) Search() clears the cache so the next call gets
// a fresh one. We don't bother with refreshJwt since accessJwt is
// valid for ~2h, which covers any reasonable batch.
func (p *SearchProvider) session(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.sessJWT != "" && time.Since(p.sessTime) < 90*time.Minute {
		jwt := p.sessJWT
		p.mu.Unlock()
		return jwt, nil
	}
	p.mu.Unlock()

	handle := p.Handle
	if handle == "" {
		handle = os.Getenv("BLUESKY_HANDLE")
	}
	pw := p.AppPassword
	if pw == "" {
		pw = os.Getenv("BLUESKY_APP_PASSWORD")
	}
	if handle == "" || pw == "" {
		return "", errors.New("bluesky search: set BLUESKY_HANDLE + BLUESKY_APP_PASSWORD (generate the app password at https://bsky.app/settings/app-passwords)")
	}

	body, err := json.Marshal(map[string]string{
		"identifier": handle,
		"password":   pw,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.authBaseURL()+"/com.atproto.server.createSession",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("bluesky session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("bluesky session: HTTP %d (check BLUESKY_HANDLE / BLUESKY_APP_PASSWORD)", resp.StatusCode)
	}
	var sess struct {
		AccessJwt string `json:"accessJwt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return "", err
	}
	if sess.AccessJwt == "" {
		return "", errors.New("bluesky session: no accessJwt in response")
	}
	p.mu.Lock()
	p.sessJWT = sess.AccessJwt
	p.sessTime = time.Now()
	p.mu.Unlock()
	return sess.AccessJwt, nil
}

func (p *SearchProvider) authBaseURL() string {
	if p.AuthBase != "" {
		return p.AuthBase
	}
	return authBase
}
