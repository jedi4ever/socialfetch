package bluesky

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jedi4ever/socialfetch/internal/core"
)

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://bsky.app/profile/jay.bsky.team/post/3kabc", true},
		{"https://bsky.app/profile/did:plc:abc/post/3kxyz", true},
		{"https://bsky.app/profile/jay.bsky.team", false},
		{"https://bsky.app/", false},
		{"https://twitter.com/x/status/1", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// Happy path: the fetcher resolves the handle, fetches the thread,
// and converts replies to nested Comments.
func TestFetchHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/com.atproto.identity.resolveHandle"):
			if r.URL.Query().Get("handle") != "jay.bsky.team" {
				t.Errorf("handle = %q", r.URL.Query().Get("handle"))
			}
			fmt.Fprint(w, `{"did":"did:plc:abc"}`)
		case strings.HasSuffix(r.URL.Path, "/app.bsky.feed.getPostThread"):
			if !strings.Contains(r.URL.Query().Get("uri"), "did:plc:abc") {
				t.Errorf("uri missing did: %q", r.URL.Query().Get("uri"))
			}
			fmt.Fprint(w, `{"thread":{
			  "post":{
			    "uri":"at://did:plc:abc/app.bsky.feed.post/3kpost",
			    "author":{"did":"did:plc:abc","handle":"jay.bsky.team","displayName":"Jay"},
			    "record":{"text":"hello world","createdAt":"2026-04-01T12:00:00Z"},
			    "likeCount":7,"replyCount":1
			  },
			  "replies":[{
			    "post":{
			      "uri":"at://did:plc:def/app.bsky.feed.post/3kreply",
			      "author":{"did":"did:plc:def","handle":"alice.bsky.social","displayName":"Alice"},
			      "record":{"text":"good point","createdAt":"2026-04-01T12:01:00Z"},
			      "likeCount":1
			    }
			  }]
			}}`)
		default:
			http.Error(w, "unexpected: "+r.URL.Path, 404)
		}
	}))
	defer srv.Close()

	f := New()
	f.BaseURL = srv.URL

	item, err := f.Fetch(context.Background(),
		"https://bsky.app/profile/jay.bsky.team/post/3kpost",
		core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Source != "bluesky" || item.Kind != "post" {
		t.Errorf("source/kind: %s/%s", item.Source, item.Kind)
	}
	if item.CanonicalID != "3kpost" {
		t.Errorf("canonical = %q", item.CanonicalID)
	}
	if item.Author != "Jay (@jay.bsky.team)" {
		t.Errorf("author = %q", item.Author)
	}
	if item.Score != 7 {
		t.Errorf("score = %d", item.Score)
	}
	if len(item.Comments) != 1 || item.Comments[0].Body != "good point" {
		t.Errorf("comments not built: %+v", item.Comments)
	}
}

// When the handle is already a DID, no resolveHandle call is made.
func TestFetchSkipsResolveForDID(t *testing.T) {
	resolveCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/com.atproto.identity.resolveHandle") {
			resolveCalls++
		}
		fmt.Fprint(w, `{"thread":{"post":{
		  "uri":"at://did:plc:abc/app.bsky.feed.post/3k",
		  "author":{"did":"did:plc:abc","handle":"x"},
		  "record":{"text":"hi","createdAt":"2026-04-01T12:00:00Z"}
		}}}`)
	}))
	defer srv.Close()
	f := New()
	f.BaseURL = srv.URL
	if _, err := f.Fetch(context.Background(),
		"https://bsky.app/profile/did:plc:abc/post/3k",
		core.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	if resolveCalls != 0 {
		t.Errorf("resolveHandle called %d times for a DID URL", resolveCalls)
	}
}
