package twitter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/search"
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

	prev := TokenURL
	TokenURL = tokenSrv.URL
	defer func() { TokenURL = prev }()
	ResetCache()

	p := NewSearchProvider()
	p.BaseURL = apiSrv.URL
	p.Creds = Credentials{Key: "k", Secret: "s"}

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

// X v2 recent-search rejects start_time older than 7 days. Catch this
// at the client so the user gets a clear message instead of HTTP 400.
func TestSearchRejectsAfterBeyondWindow(t *testing.T) {
	p := NewSearchProvider()
	p.Creds = Credentials{Key: "k", Secret: "s"}
	tooOld := time.Now().UTC().AddDate(0, 0, -14)
	_, err := p.Search(context.Background(), "x", search.Options{Max: 10, After: &tooOld})
	if err == nil {
		t.Fatal("expected error for after > 7 days")
	}
	if !strings.Contains(err.Error(), "7 days") {
		t.Errorf("error should name the 7-day limit: %v", err)
	}
}

// Non-2xx responses from X must surface the underlying message
// (errors[].message or {title,detail}), not just the status code.
func TestSearchSurfacesErrorBody(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"BEARER"}`))
	}))
	defer tokenSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Invalid 'start_time': value too old"}]}`))
	}))
	defer apiSrv.Close()

	prev := TokenURL
	TokenURL = tokenSrv.URL
	defer func() { TokenURL = prev }()
	ResetCache()

	p := NewSearchProvider()
	p.BaseURL = apiSrv.URL
	p.Creds = Credentials{Key: "k", Secret: "s"}

	_, err := p.Search(context.Background(), "x", search.Options{Max: 10})
	if err == nil {
		t.Fatal("expected error from 400 response")
	}
	if !strings.Contains(err.Error(), "Invalid 'start_time'") {
		t.Errorf("error must include API message, got: %v", err)
	}
}

func TestSearchRequiresCreds(t *testing.T) {
	t.Setenv("X_API_KEY", "")
	t.Setenv("X_API_SECRET", "")
	if _, err := NewSearchProvider().Search(context.Background(), "x", search.Options{Max: 10}); err == nil {
		t.Errorf("expected creds error")
	}
}
