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
