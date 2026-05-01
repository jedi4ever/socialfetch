package bravesearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/patrickdebois/social-skills/internal/search"
)

func TestSearchRequiresKey(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	if _, err := New().Search(context.Background(), "x", search.Options{Max: 5}); err == nil {
		t.Errorf("expected missing-key error")
	}
}

func TestSearchHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "K" {
			t.Errorf("token header not set: %q", r.Header.Get("X-Subscription-Token"))
		}
		if r.URL.Query().Get("q") != "anthropic" {
			t.Errorf("q = %q", r.URL.Query().Get("q"))
		}
		fmt.Fprint(w, `{
		  "web":{"results":[
		    {"title":"A","url":"https://a.example","description":"first","page_age":"2026-04-15T10:00:00Z"},
		    {"title":"B","url":"https://b.example","description":"second"}
		  ]}
		}`)
	}))
	defer srv.Close()
	p := New()
	p.BaseURL = srv.URL
	p.Key = "K"

	got, err := p.Search(context.Background(), "anthropic", search.Options{Max: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].Published == nil {
		t.Errorf("first result Published should be parsed")
	}
}

// `freshness` is computed from opts.After: single-day → pd, week → pw,
// month → pm, year → py, otherwise an explicit date range.
func TestFreshnessMappingPresets(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{20 * time.Hour, "pd"},
		{6 * 24 * time.Hour, "pw"},
		{20 * 24 * time.Hour, "pm"},
		{200 * 24 * time.Hour, "py"},
	}
	for _, c := range cases {
		got := freshnessFor(now.Add(-c.ago), nil)
		if got != c.want {
			t.Errorf("ago=%v: got %q, want %q", c.ago, got, c.want)
		}
	}
}

// 401 surfaces a useful "key rejected" message.
func TestSearch401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", 401)
	}))
	defer srv.Close()
	p := New()
	p.BaseURL = srv.URL
	p.Key = "bad"
	_, err := p.Search(context.Background(), "x", search.Options{Max: 5})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("want 401 error, got %v", err)
	}
}
