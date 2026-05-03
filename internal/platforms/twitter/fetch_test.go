package twitter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
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
		if strings.HasPrefix(r.URL.Path, "/tweets/search/recent") {
			// reply_count=2 triggers a follow-up search; serve an empty
			// page so the main-tweet assertions stay focused.
			fmt.Fprint(w, `{"data":[],"meta":{"result_count":0}}`)
			return
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

	prev := TokenURL
	TokenURL = tokenSrv.URL
	defer func() { TokenURL = prev }()
	ResetCache()

	f := New()
	f.APIBaseURL = apiSrv.URL
	f.BaseURL = "http://127.0.0.1:1" // would fail if used; v2 should win
	f.Creds = Credentials{Key: "k", Secret: "s"}

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

// With creds set and IncludeComments=true, the v2 path should issue one
// search/recent call after the main fetch and assemble a reply tree.
func TestFetchViaV2APIWithReplies(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"BEARER"}`))
	}))
	defer tokenSrv.Close()

	var searchCalls int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/tweets/search/recent"):
			searchCalls++
			if got := r.URL.Query().Get("query"); got != "conversation_id:77" {
				t.Errorf("wrong query: %q", got)
			}
			fmt.Fprint(w, `{
			  "data": [
			    {"id":"100","author_id":"u2","text":"reply A","created_at":"2026-04-01T12:01:00.000Z","public_metrics":{"like_count":3},"referenced_tweets":[{"type":"replied_to","id":"77"}]},
			    {"id":"101","author_id":"u3","text":"reply B","created_at":"2026-04-01T12:02:00.000Z","public_metrics":{"like_count":1},"referenced_tweets":[{"type":"replied_to","id":"77"}]},
			    {"id":"200","author_id":"u4","text":"nested under A","created_at":"2026-04-01T12:03:00.000Z","public_metrics":{"like_count":0},"referenced_tweets":[{"type":"replied_to","id":"100"}]}
			  ],
			  "includes": {"users":[
			    {"id":"u2","name":"Bob","username":"bob"},
			    {"id":"u3","name":"Carol","username":"carol"},
			    {"id":"u4","name":"Dan","username":"dan"}
			  ]},
			  "meta": {"result_count": 3}
			}`)
		case strings.Contains(r.URL.Path, "/tweets/77"):
			fmt.Fprint(w, `{
			  "data": {
			    "id": "77", "author_id": "u1", "conversation_id": "77",
			    "text": "root tweet",
			    "created_at": "2026-04-01T12:00:00.000Z",
			    "lang": "en",
			    "public_metrics": {"like_count": 99, "retweet_count": 1, "reply_count": 3}
			  },
			  "includes": {"users":[{"id":"u1","name":"Jane","username":"jane"}]}
			}`)
		default:
			http.Error(w, "unexpected: "+r.URL.Path, 404)
		}
	}))
	defer apiSrv.Close()

	prev := TokenURL
	TokenURL = tokenSrv.URL
	defer func() { TokenURL = prev }()
	ResetCache()

	f := New()
	f.APIBaseURL = apiSrv.URL
	f.BaseURL = "http://127.0.0.1:1"
	f.Creds = Credentials{Key: "k", Secret: "s"}

	item, err := f.Fetch(context.Background(), "https://x.com/jane/status/77", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if searchCalls != 1 {
		t.Errorf("expected 1 search call, got %d", searchCalls)
	}
	if got := len(item.Comments); got != 2 {
		t.Fatalf("want 2 top-level comments, got %d", got)
	}
	// Reply A (id=100) should have one nested reply (id=200).
	var a *core.Comment
	for i := range item.Comments {
		if item.Comments[i].ID == "100" {
			a = &item.Comments[i]
			break
		}
	}
	if a == nil {
		t.Fatalf("comment 100 not found")
	}
	if got := len(a.Replies); got != 1 || a.Replies[0].ID != "200" {
		t.Errorf("nested reply not built: %+v", a.Replies)
	}
	if a.Replies[0].Depth != 1 {
		t.Errorf("nested depth = %d, want 1", a.Replies[0].Depth)
	}
	if !strings.Contains(a.Author, "Bob") || !strings.Contains(a.Author, "@bob") {
		t.Errorf("author format: %q", a.Author)
	}
}

// High-engagement tweets occasionally come back with parent refs that
// form cycles (A→B→A) — typically quote-tweet misclassification or
// deleted-parent edge cases. Without cycle detection, attach()
// infinite-recurses and blows the stack. This test feeds a synthetic
// cycle through the v2 path and asserts Fetch returns cleanly.
func TestFetchViaV2APIRepliesCycleSafe(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"BEARER"}`))
	}))
	defer tokenSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/tweets/search/recent"):
			// Models a realistic pagination edge case where the same
			// reply id (100) shows up twice in the result set with
			// different replied_to references — this is the failure
			// shape we've seen on viral threads. The duplicate creates
			// a cycle reachable from root: 77 → 100 → 200 → 100 → ...
			//
			//   record 1: 100 replied_to 77   (legit, attaches under root)
			//   record 2: 200 replied_to 100
			//   record 3: 100 replied_to 200  (duplicate id, cycles back)
			//
			// Without cycle detection, attach() infinite-recurses.
			fmt.Fprint(w, `{
			  "data": [
			    {"id":"100","author_id":"u2","text":"reply A","created_at":"2026-04-01T12:01:00.000Z","public_metrics":{"like_count":1},"referenced_tweets":[{"type":"replied_to","id":"77"}]},
			    {"id":"200","author_id":"u3","text":"reply B","created_at":"2026-04-01T12:02:00.000Z","public_metrics":{"like_count":1},"referenced_tweets":[{"type":"replied_to","id":"100"}]},
			    {"id":"100","author_id":"u2","text":"reply A (paginated dupe)","created_at":"2026-04-01T12:01:00.000Z","public_metrics":{"like_count":1},"referenced_tweets":[{"type":"replied_to","id":"200"}]}
			  ],
			  "includes": {"users":[
			    {"id":"u2","name":"Bob","username":"bob"},
			    {"id":"u3","name":"Carol","username":"carol"}
			  ]},
			  "meta": {"result_count": 3}
			}`)
		case strings.Contains(r.URL.Path, "/tweets/77"):
			fmt.Fprint(w, `{
			  "data": {"id":"77","author_id":"u1","conversation_id":"77","text":"root","created_at":"2026-04-01T12:00:00.000Z","lang":"en","public_metrics":{"like_count":1,"retweet_count":0,"reply_count":3}},
			  "includes": {"users":[{"id":"u1","name":"Jane","username":"jane"}]}
			}`)
		default:
			http.Error(w, "unexpected: "+r.URL.Path, 404)
		}
	}))
	defer apiSrv.Close()

	prev := TokenURL
	TokenURL = tokenSrv.URL
	defer func() { TokenURL = prev }()
	ResetCache()

	f := New()
	f.APIBaseURL = apiSrv.URL
	f.BaseURL = "http://127.0.0.1:1"
	f.Creds = Credentials{Key: "k", Secret: "s"}

	// The whole point: returns at all (no infinite recursion).
	item, err := f.Fetch(context.Background(), "https://x.com/jane/status/77", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Cycle nodes should each appear exactly once across the tree.
	seen := map[string]int{}
	var walk func([]core.Comment)
	walk = func(cs []core.Comment) {
		for _, c := range cs {
			seen[c.ID]++
			walk(c.Replies)
		}
	}
	walk(item.Comments)
	for _, id := range []string{"100", "200"} {
		if seen[id] != 1 {
			t.Errorf("comment %s appeared %d times, want 1 (cycle should be deduped)", id, seen[id])
		}
	}
}

// IncludeComments=false should skip the search call entirely.
func TestFetchViaV2APINoReplies(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"BEARER"}`))
	}))
	defer tokenSrv.Close()

	var searchCalls int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tweets/search/recent") {
			searchCalls++
		}
		if strings.HasPrefix(r.URL.Path, "/tweets/search/recent") {
			fmt.Fprint(w, `{"data":[],"meta":{"result_count":0}}`)
			return
		}
		fmt.Fprint(w, `{
		  "data":{"id":"77","author_id":"u1","conversation_id":"77","text":"root","created_at":"2026-04-01T12:00:00.000Z","public_metrics":{"like_count":1,"reply_count":3}},
		  "includes":{"users":[{"id":"u1","name":"Jane","username":"jane"}]}
		}`)
	}))
	defer apiSrv.Close()

	prev := TokenURL
	TokenURL = tokenSrv.URL
	defer func() { TokenURL = prev }()
	ResetCache()

	f := New()
	f.APIBaseURL = apiSrv.URL
	f.BaseURL = "http://127.0.0.1:1"
	f.Creds = Credentials{Key: "k", Secret: "s"}

	opts := core.DefaultOptions()
	opts.IncludeComments = false
	if _, err := f.Fetch(context.Background(), "https://x.com/jane/status/77", opts); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if searchCalls != 0 {
		t.Errorf("search called %d times with IncludeComments=false", searchCalls)
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
