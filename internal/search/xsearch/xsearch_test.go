package xsearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/patrickdebois/social-skills/internal/search"
	"github.com/patrickdebois/social-skills/internal/xauth"
)

const fakeJSON = `{
  "data": [
    {
      "id": "100",
      "text": "Short tweet",
      "author_id": "u1",
      "created_at": "2026-04-01T12:00:00.000Z",
      "public_metrics": {"like_count": 10, "retweet_count": 1, "reply_count": 2}
    },
    {
      "id": "101",
      "text": "stub",
      "author_id": "u2",
      "created_at": "2026-04-01T13:00:00.000Z",
      "note_tweet": {"text": "Long-form note text here that goes past 280 chars."},
      "public_metrics": {"like_count": 99, "retweet_count": 5, "reply_count": 7}
    }
  ],
  "includes": {
    "users": [
      {"id": "u1", "name": "Alice", "username": "alice"},
      {"id": "u2", "name": "Bob",   "username": "bob"}
    ]
  }
}`

func newFakeAPI(t *testing.T) (*httptest.Server, *httptest.Server) {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"BEARER"}`))
	}))
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer BEARER" {
			t.Errorf("missing/wrong bearer: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("query") != "vibe coding" {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeJSON))
	}))
	return tokenSrv, apiSrv
}

func TestSearch(t *testing.T) {
	tokenSrv, apiSrv := newFakeAPI(t)
	defer tokenSrv.Close()
	defer apiSrv.Close()

	prev := xauth.TokenURL
	xauth.TokenURL = tokenSrv.URL
	defer func() { xauth.TokenURL = prev }()
	xauth.ResetCache()

	p := New()
	p.BaseURL = apiSrv.URL
	p.Creds = xauth.Credentials{Key: "k", Secret: "s"}

	got, err := p.Search(context.Background(), "vibe coding", search.Options{Max: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].Title != "@alice" || got[0].URL != "https://x.com/alice/status/100" {
		t.Errorf("first: %+v", got[0])
	}
	// Long-form tweet must use note_tweet.text instead of stub `text`.
	if !strings.Contains(got[1].Snippet, "Long-form note text") {
		t.Errorf("note_tweet not preferred: %q", got[1].Snippet)
	}
	if got[1].URL != "https://x.com/bob/status/101" {
		t.Errorf("second URL: %q", got[1].URL)
	}
}

func TestSearchRequiresCreds(t *testing.T) {
	t.Setenv("X_API_KEY", "")
	t.Setenv("X_API_SECRET", "")
	if _, err := New().Search(context.Background(), "x", search.Options{Max: 10}); err == nil {
		t.Errorf("expected creds error")
	}
}
