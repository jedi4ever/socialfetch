package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
)

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://github.com/anthropics/claude-code", true},
		{"https://github.com/anthropics/claude-code/issues/1", true},
		{"https://github.com/", false},
		{"https://github.com/anthropics", false},
		{"https://news.ycombinator.com/", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestSplitOwnerRepo(t *testing.T) {
	o, r := splitOwnerRepo("/foo/bar")
	if o != "foo" || r != "bar" {
		t.Errorf("got %q/%q", o, r)
	}
	o, r = splitOwnerRepo("/foo/bar.git")
	if r != "bar" {
		t.Errorf("did not strip .git: %q", r)
	}
	o, r = splitOwnerRepo("/foo")
	if o != "" || r != "" {
		t.Errorf("expected empty for incomplete path")
	}
}

func TestFetchRepo(t *testing.T) {
	readme := base64.StdEncoding.EncodeToString([]byte("# Hello\n\nReadme body"))

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/foo/bar", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"name":"bar","full_name":"foo/bar","description":"the bar repo",
			"homepage":"https://example.com","html_url":"https://github.com/foo/bar",
			"default_branch":"main","language":"Go","topics":["cli","tools"],
			"license":{"spdx_id":"MIT"},
			"stargazers_count":123,"forks_count":4,"open_issues_count":2,"watchers_count":123,
			"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-01T00:00:00Z","pushed_at":"2024-06-01T00:00:00Z",
			"private":false,"fork":false,"archived":false,
			"owner":{"login":"foo","type":"User","html_url":"https://github.com/foo"}
		}`)
	})
	mux.HandleFunc("/repos/foo/bar/readme", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"name":"README.md","path":"README.md","size":42,"encoding":"base64","content":"%s"}`, readme)
	})
	mux.HandleFunc("/repos/foo/bar/releases", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"tag_name":"v1.0.0","name":"v1","published_at":"2024-05-01T00:00:00Z","prerelease":false,"body":"changes"}]`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := New()
	f.BaseURL = srv.URL

	item, err := f.Fetch(context.Background(), "https://github.com/foo/bar", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Title != "foo/bar" {
		t.Errorf("title: %q", item.Title)
	}
	if !strings.Contains(item.Content, "Readme body") {
		t.Errorf("readme not decoded: %q", item.Content)
	}
	if item.Score != 123 {
		t.Errorf("stars: %d", item.Score)
	}
	if item.Extra["license"] != "MIT" {
		t.Errorf("license: %v", item.Extra["license"])
	}
	rels, _ := item.Extra["recent_releases"].([]map[string]any)
	if len(rels) != 1 || rels[0]["tag"] != "v1.0.0" {
		t.Errorf("releases: %+v", rels)
	}
}
