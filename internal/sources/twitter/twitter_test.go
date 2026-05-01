package twitter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/xauth"
)

const fakeTweet = `{
  "id_str": "1500000000000000000",
  "text": "Hello https://t.co/abc world",
  "created_at": "Wed Oct 10 10:10:10 +0000 2024",
  "lang": "en",
  "user": {"name": "Jane Doe", "screen_name": "janedoe", "profile_image_url_https": "https://pbs.twimg.com/janedoe.jpg"},
  "photos": [{"url": "https://pbs.twimg.com/media/abc.jpg"}],
  "video": {
    "variants": [
      {"type": "video/mp4", "src": "https://video.twimg.com/low.mp4", "bitrate": 320000},
      {"type": "video/mp4", "src": "https://video.twimg.com/hi.mp4", "bitrate": 1280000},
      {"type": "application/x-mpegURL", "src": "https://video.twimg.com/index.m3u8"}
    ]
  },
  "entities": {"urls": [{"url": "https://t.co/abc", "expanded_url": "https://example.com/blog/post"}]},
  "favorite_count": 42,
  "conversation_count": 7
}`

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://twitter.com/jane/status/12345", true},
		{"https://x.com/jane/status/12345", true},
		{"https://mobile.twitter.com/jane/status/12345", true},
		{"https://x.com/jane", false},
		{"https://example.com/", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestExtractID(t *testing.T) {
	id, err := extractID("https://x.com/jane/status/12345")
	if err != nil || id != "12345" {
		t.Errorf("got %q/%v", id, err)
	}
	if _, err := extractID("https://x.com/jane"); err == nil {
		t.Errorf("expected error")
	}
}

func TestSyndicationToken(t *testing.T) {
	// We can't validate the exact token against a black-box reference
	// without running V8, but we can assert the shape: alphanumeric,
	// no zeros, no dots, non-empty for any plausible tweet ID.
	tok := syndicationToken("1500000000000000000")
	if tok == "" {
		t.Fatalf("token empty")
	}
	if strings.ContainsAny(tok, "0.") {
		t.Errorf("token still contains stripped chars: %q", tok)
	}
	for _, c := range tok {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
			t.Errorf("non-base36 char in token: %q", tok)
		}
	}
}

// API v2 happy path: with credentials set, the fetcher hits the official
// API and uses note_tweet for long-form content. The syndication endpoint
// is never called when v2 succeeds.
func TestFetchViaV2API(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"BEARER"}`))
	}))
	defer tokenSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer BEARER" {
			t.Errorf("missing/wrong bearer: %q", r.Header.Get("Authorization"))
		}
		if !strings.Contains(r.URL.Path, "/tweets/77") {
			t.Errorf("wrong endpoint: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{
		  "data": {
		    "id": "77", "author_id": "u1",
		    "text": "stub text",
		    "created_at": "2026-04-01T12:00:00.000Z",
		    "lang": "en",
		    "public_metrics": {"like_count": 99, "retweet_count": 1, "reply_count": 2},
		    "note_tweet": {"text": "Long-form body that goes past the 280-char limit."},
		    "entities": {"urls": [{"url":"https://t.co/x","expanded_url":"https://example.com/blog"}]}
		  },
		  "includes": {
		    "users": [{"id":"u1","name":"Jane Doe","username":"jane"}],
		    "media": [{"media_key":"m1","type":"photo","url":"https://pbs.twimg.com/abc.jpg"}]
		  }
		}`)
	}))
	defer apiSrv.Close()

	prev := xauth.TokenURL
	xauth.TokenURL = tokenSrv.URL
	defer func() { xauth.TokenURL = prev }()
	xauth.ResetCache()

	f := New()
	f.APIBaseURL = apiSrv.URL
	f.BaseURL = "http://127.0.0.1:1" // would fail if used; v2 should win
	f.Creds = xauth.Credentials{Key: "k", Secret: "s"}

	item, err := f.Fetch(context.Background(), "https://x.com/jane/status/77", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(item.Content, "Long-form body") {
		t.Errorf("note_tweet not used: %q", item.Content)
	}
	if item.Author != "Jane Doe" || item.Score != 99 {
		t.Errorf("unexpected: %+v", item)
	}
	if got := item.Extra["via"]; got != "v2_api" {
		t.Errorf("expected via=v2_api, got %v", got)
	}
}

func TestFetchTweet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tweet-result", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "12345" {
			http.Error(w, "wrong id", 404)
			return
		}
		fmt.Fprint(w, fakeTweet)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := New()
	f.BaseURL = srv.URL

	item, err := f.Fetch(context.Background(), "https://x.com/jane/status/12345", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if item.Author != "Jane Doe" {
		t.Errorf("author: %q", item.Author)
	}
	if !strings.Contains(item.Content, "https://example.com/blog/post") {
		t.Errorf("t.co not expanded: %q", item.Content)
	}
	if got := len(item.Media); got != 2 {
		t.Fatalf("want 2 media (photo + video), got %d", got)
	}
	// Highest-bitrate mp4 wins.
	if item.Media[1].URL != "https://video.twimg.com/hi.mp4" {
		t.Errorf("wrong video pick: %q", item.Media[1].URL)
	}
	if item.Score != 42 {
		t.Errorf("score: %d", item.Score)
	}
}
