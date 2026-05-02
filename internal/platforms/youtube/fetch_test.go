package youtube

import (
	"context"
	"encoding/json"
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
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"https://youtu.be/dQw4w9WgXcQ", true},
		{"https://m.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"https://www.youtube.com/shorts/dQw4w9WgXcQ", true},
		{"https://www.youtube.com/live/dQw4w9WgXcQ", true},
		{"https://www.youtube.com/embed/dQw4w9WgXcQ", true},
		{"https://music.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"https://www.youtube.com/", false},                 // home page, no id
		{"https://www.youtube.com/@channel", false},         // channel, not a video
		{"https://www.youtube.com/watch?v=tooshort", false}, // 8-char id rejected
		{"https://example.com/watch?v=dQw4w9WgXcQ", false},  // wrong host
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestExtractID(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42", "dQw4w9WgXcQ"},
		{"https://youtu.be/dQw4w9WgXcQ?si=abc", "dQw4w9WgXcQ"},
		{"https://www.youtube.com/shorts/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"https://www.youtube.com/live/dQw4w9WgXcQ/?feature=share", "dQw4w9WgXcQ"},
		{"https://www.youtube.com/playlist?list=PLabc", ""},
		{"https://www.youtube.com/watch?v=tooshort", ""},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := extractIDFromURL(u); got != c.want {
			t.Errorf("extractIDFromURL(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// CommentThreads happy path: with API key set, the fetcher hits the
// mocked endpoint, builds a tree (top-level + replies), and respects
// MaxComments.
func TestFetchCommentsBuildsTree(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/commentThreads") {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("videoId") != "abc" {
			t.Errorf("videoId = %q", r.URL.Query().Get("videoId"))
		}
		if r.URL.Query().Get("key") != "K" {
			t.Errorf("key not forwarded")
		}
		// Single page, two threads, second one has a reply.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"snippet": map[string]any{
						"topLevelComment": map[string]any{
							"id": "T1",
							"snippet": map[string]any{
								"authorDisplayName": "Alice",
								"textOriginal":      "first take",
								"likeCount":         3,
								"publishedAt":       "2026-04-01T12:00:00Z",
							},
						},
						"totalReplyCount": 0,
					},
				},
				{
					"snippet": map[string]any{
						"topLevelComment": map[string]any{
							"id": "T2",
							"snippet": map[string]any{
								"authorDisplayName": "Bob",
								"textOriginal":      "second take",
								"likeCount":         1,
								"publishedAt":       "2026-04-01T12:01:00Z",
							},
						},
						"totalReplyCount": 1,
					},
					"replies": map[string]any{
						"comments": []map[string]any{
							{
								"id": "R1",
								"snippet": map[string]any{
									"authorDisplayName": "Carol",
									"textOriginal":      "good point",
									"likeCount":         0,
									"publishedAt":       "2026-04-01T12:02:00Z",
								},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	f := New()
	f.CommentsBase = srv.URL
	f.APIKey = "K"

	got, err := f.fetchComments(context.Background(), "abc", "K", core.DefaultOptions())
	if err != nil {
		t.Fatalf("fetchComments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 top-level comments, got %d", len(got))
	}
	if got[0].Author != "Alice" || got[0].Body != "first take" || got[0].Score != 3 {
		t.Errorf("top-level comment 0: %+v", got[0])
	}
	if len(got[1].Replies) != 1 || got[1].Replies[0].Author != "Carol" {
		t.Errorf("reply not attached: %+v", got[1].Replies)
	}
	if got[1].Replies[0].Depth != 1 {
		t.Errorf("reply depth = %d, want 1", got[1].Replies[0].Depth)
	}
	if calls != 1 {
		t.Errorf("expected 1 API call, got %d", calls)
	}
}

// 403 from the API surfaces a helpful error mentioning quota / disabled
// comments, since both routes return 403.
func TestFetchComments403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", 403)
	}))
	defer srv.Close()

	f := New()
	f.CommentsBase = srv.URL
	f.APIKey = "K"

	_, err := f.fetchComments(context.Background(), "abc", "K", core.DefaultOptions())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("want 403 error, got %v", err)
	}
}
