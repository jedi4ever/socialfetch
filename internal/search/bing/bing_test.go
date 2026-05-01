package bing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/patrickdebois/social-skills/internal/search"
)

const fakeJSON = `{
  "webPages": {
    "value": [
      {"name": "First Result", "url": "https://example.com/1", "snippet": "snip 1"},
      {"name": "Second", "url": "https://example.com/2", "snippet": "snip 2"}
    ]
  }
}`

func TestSearchRequiresKey(t *testing.T) {
	t.Setenv("BING_API_KEY", "")
	if _, err := New().Search(context.Background(), "x", search.Options{Max: 5}); err == nil {
		t.Errorf("expected missing-key error")
	}
}

func TestSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Ocp-Apim-Subscription-Key") != "secret" {
			t.Errorf("missing or wrong key: %q", r.Header.Get("Ocp-Apim-Subscription-Key"))
		}
		if r.URL.Query().Get("q") != "anthropic" {
			t.Errorf("missing query: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("count") != "5" {
			t.Errorf("count not forwarded: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeJSON))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "secret"

	got, err := p.Search(context.Background(), "anthropic", search.Options{Max: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].URL != "https://example.com/1" || got[0].Source != "bing" {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].Title != "Second" {
		t.Errorf("second: %+v", got[1])
	}
}

func TestSearchPropagatesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Quota exceeded"}]}`))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "any"

	_, err := p.Search(context.Background(), "x", search.Options{Max: 5})
	if err == nil || !strings.Contains(err.Error(), "Quota exceeded") {
		t.Errorf("want quota error, got %v", err)
	}
}

func TestSearchCapsAtMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeJSON))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL
	p.Key = "any"

	got, err := p.Search(context.Background(), "x", search.Options{Max: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("max=1 ignored: got %d", len(got))
	}
}
