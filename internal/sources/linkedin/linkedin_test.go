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

	"github.com/patrickdebois/social-skills/internal/core"
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

// 503 from the bridge surfaces a useful "extension not connected" error.
func TestFetchBridgeNotConnected(t *testing.T) {
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

// status:"error" from the extension is surfaced cleanly.
func TestFetchExtensionError(t *testing.T) {
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
