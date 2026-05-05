package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
	"golang.org/x/net/html"
)

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://www.linkedin.com/posts/jane_some-slug-7000000000000000000", true},
		{"https://linkedin.com/feed/update/urn:li:activity:7000000000000000000/", true},
		{"https://www.linkedin.com/in/janedoe/", true},
		{"https://www.linkedin.com/pulse/some-article", true},
		{"https://example.com/", false},
		{"https://twitter.com/jane/status/1", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// Happy path: the fetcher posts navigate first, then get_html, and
// converts the returned HTML into a core.Item with author/canonical id.
func TestFetchHappyPath(t *testing.T) {
	var commands []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		cmd, _ := got["command"].(string)
		commands = append(commands, cmd)
		switch cmd {
		case "navigate":
			fmt.Fprint(w, `{"status":"ok","url":"https://example"}`)
		case "get_html":
			fmt.Fprint(w, `{
			  "status":"ok",
			  "html":"<html><head><meta property=\"og:title\" content=\"Jane Doe on LinkedIn: a great post\"/></head><body><a href=\"https://www.linkedin.com/in/janedoe/\">Jane Doe</a><p>This is the body.</p></body></html>",
			  "url":"https://www.linkedin.com/posts/janedoe_a-great-post-activity-7000000000000000000",
			  "title":"Jane Doe on LinkedIn"
			}`)
		default:
			t.Errorf("unexpected command %q", cmd)
		}
	}))
	defer srv.Close()

	f := New()
	f.BridgeURL = srv.URL + "/cmd"

	item, err := f.Fetch(context.Background(),
		"https://www.linkedin.com/posts/janedoe_a-great-post-activity-7000000000000000000",
		core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Source != "linkedin" || item.Kind != "post" {
		t.Errorf("source/kind: %s/%s", item.Source, item.Kind)
	}
	if item.Author != "Jane Doe" {
		t.Errorf("author = %q", item.Author)
	}
	if item.AuthorURL != "https://www.linkedin.com/in/janedoe" {
		t.Errorf("authorURL = %q", item.AuthorURL)
	}
	if item.CanonicalID != "7000000000000000000" {
		t.Errorf("canonical = %q", item.CanonicalID)
	}
	if !strings.Contains(item.Content, "This is the body.") {
		t.Errorf("body missing: %q", item.Content)
	}
	if len(commands) != 2 || commands[0] != "navigate" || commands[1] != "get_html" {
		t.Errorf("expected navigate then get_html, got %v", commands)
	}
}

// Comment scraper picks up the author name from LinkedIn's current DOM
// shape (comments-comment-meta__description-title) plus the legacy ones.
// This locks the selector list against silent regression when LinkedIn
// renames classes — symptom is every commenter rendering as "anon".
func TestExtractCommentsAuthor(t *testing.T) {
	cases := []struct {
		name     string
		fragment string
	}{
		{
			name: "current shape (description-title)",
			fragment: `<div class="comments-comments-list">
				<article class="comments-comment-entity">
				  <a href="https://www.linkedin.com/in/janedoe" class="comments-comment-meta__description-container">
				    <h3 class="comments-comment-meta__description">
				      <span class="comments-comment-meta__description-title">Jane Doe</span>
				      <span class="comments-comment-meta__data">1st • Engineer</span>
				    </h3>
				  </a>
				  <div class="comments-comment-item__main-content">hello world</div>
				</article>
			</div>`,
		},
		{
			name: "legacy shape (actor-name)",
			fragment: `<div class="comments-comments-list">
				<article class="comments-comment-entity">
				  <a href="https://www.linkedin.com/in/janedoe"><span class="comments-comment-meta__actor-name">Jane Doe</span></a>
				  <div class="comments-comment-item__main-content">hello world</div>
				</article>
			</div>`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(c.fragment))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := extractComments(doc)
			if len(got) != 1 {
				t.Fatalf("expected 1 comment, got %d", len(got))
			}
			if got[0].Author != "Jane Doe" {
				t.Errorf("author = %q, want %q", got[0].Author, "Jane Doe")
			}
			if !strings.Contains(got[0].ID, "/in/janedoe") {
				t.Errorf("id = %q, want to contain /in/janedoe", got[0].ID)
			}
			if !strings.Contains(got[0].Body, "hello world") {
				t.Errorf("body = %q, want to contain hello world", got[0].Body)
			}
		})
	}
}

// 503 from the bridge surfaces a useful "extension not connected" error.
// Forces bridge-only chain so the assertion runs against the bridge
// runner's error, not the chain's aggregated message after a Jina
// fallback.
func TestFetchBridgeNotConnected(t *testing.T) {
	t.Setenv("SOCIAL_FETCH_CHAIN_LINKEDIN", "bridge")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no extension", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	f := New()
	f.BridgeURL = srv.URL + "/cmd"
	_, err := f.Fetch(context.Background(),
		"https://www.linkedin.com/posts/foo-activity-7000000000000000000",
		core.DefaultOptions())
	if err == nil || !strings.Contains(err.Error(), "no extension attached") {
		t.Errorf("expected helpful 503 error, got %v", err)
	}
}

// status:"error" from the extension is surfaced cleanly. Bridge-only
// chain (see TestFetchBridgeNotConnected for rationale).
func TestFetchExtensionError(t *testing.T) {
	t.Setenv("SOCIAL_FETCH_CHAIN_LINKEDIN", "bridge")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"error","error":"timeout navigating"}`)
	}))
	defer srv.Close()

	f := New()
	f.BridgeURL = srv.URL + "/cmd"
	_, err := f.Fetch(context.Background(),
		"https://www.linkedin.com/posts/x",
		core.DefaultOptions())
	if err == nil || !strings.Contains(err.Error(), "timeout navigating") {
		t.Errorf("expected extension error, got %v", err)
	}
}
