package reddit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
)

const fakeResponse = `[
  {
    "kind": "Listing",
    "data": {
      "children": [
        {
          "kind": "t3",
          "data": {
            "id": "abc123",
            "title": "Hello, Reddit",
            "url": "https://example.com/post",
            "selftext": "Body text here.",
            "author": "alice",
            "subreddit": "programming",
            "score": 99,
            "upvote_ratio": 0.95,
            "num_comments": 2,
            "created_utc": 1700000000,
            "permalink": "/r/programming/comments/abc123/hello/",
            "is_self": false,
            "link_flair_text": "Discussion",
            "preview": {
              "images": [
                {"source": {"url": "https://i.redd.it/x.jpg?amp=1&amp;s=2"}}
              ]
            }
          }
        }
      ]
    }
  },
  {
    "kind": "Listing",
    "data": {
      "children": [
        {
          "kind": "t1",
          "data": {
            "id": "c1",
            "author": "bob",
            "body": "First!",
            "score": 5,
            "created_utc": 1700000100,
            "replies": {
              "kind": "Listing",
              "data": {
                "children": [
                  {
                    "kind": "t1",
                    "data": {
                      "id": "c2",
                      "author": "carol",
                      "body": "Reply",
                      "score": 2,
                      "created_utc": 1700000200,
                      "replies": ""
                    }
                  }
                ]
              }
            }
          }
        },
        {"kind": "more", "data": {"id": "x"}}
      ]
    }
  }
]`

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://www.reddit.com/r/programming/comments/abc/hello/", true},
		{"https://old.reddit.com/r/foo/comments/xyz/title/", true},
		{"https://www.reddit.com/r/programming/", false},
		{"https://reddit.com/", false},
		{"https://news.ycombinator.com/item?id=1", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestFetchPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept any path ending in .json, since we rewrite to it.
		if !strings.HasSuffix(r.URL.Path, ".json") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeResponse))
	}))
	defer srv.Close()

	f := New()
	f.BaseURL = srv.URL

	item, err := f.Fetch(context.Background(), "https://www.reddit.com/r/programming/comments/abc123/hello/", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if item.Source != "reddit" || item.Title != "Hello, Reddit" {
		t.Errorf("unexpected item: %+v", item)
	}
	if item.Author != "alice" || item.Score != 99 {
		t.Errorf("unexpected author/score: %+v", item)
	}
	if got := len(item.Comments); got != 1 {
		t.Fatalf("want 1 top-level comment (more stub dropped), got %d", got)
	}
	if got := len(item.Comments[0].Replies); got != 1 {
		t.Fatalf("want 1 reply, got %d", got)
	}
	if item.Comments[0].Replies[0].Body != "Reply" {
		t.Errorf("unexpected nested reply: %+v", item.Comments[0].Replies[0])
	}
	// &amp; must be unescaped.
	if got := item.Media[0].URL; strings.Contains(got, "&amp;") {
		t.Errorf("media URL still HTML-escaped: %q", got)
	}
}

func TestJSONURLForRewritesHost(t *testing.T) {
	out, err := jsonURLFor("https://www.reddit.com/r/programming/comments/abc/hello/", "http://127.0.0.1:9999")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "http://127.0.0.1:9999/r/programming/comments/abc/hello.json") {
		t.Errorf("unexpected rewrite: %s", out)
	}
}
