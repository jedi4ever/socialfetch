package duckduckgo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/patrickdebois/social-skills/internal/core"
)

const fakeHTML = `<html><body><table>
<tr><td><a class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&amp;rut=x">Example A</a></td></tr>
<tr><td class="result-snippet">First snippet here.</td></tr>
<tr><td><a class="result-link" href="https://example.org/b">Example B</a></td></tr>
<tr><td class="result-snippet">Second snippet.</td></tr>
<tr><td><a class="other">ignored</a></td></tr>
</table></body></html>`

func TestSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Query().Get("q") != "golang fetch" {
			t.Errorf("missing query param: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fakeHTML))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL + "/"

	results, err := p.Search(context.Background(), "golang fetch", core.SearchOptions{Max: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(results), results)
	}
	if results[0].URL != "https://example.com/a" {
		t.Errorf("redirect not unwrapped: %q", results[0].URL)
	}
	if results[0].Title != "Example A" || results[0].Snippet != "First snippet here." {
		t.Errorf("first result: %+v", results[0])
	}
	if results[1].URL != "https://example.org/b" || results[1].Source != "duckduckgo" {
		t.Errorf("second result: %+v", results[1])
	}
}

func TestSearchRespectsMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeHTML))
	}))
	defer srv.Close()

	p := New()
	p.BaseURL = srv.URL + "/"

	results, err := p.Search(context.Background(), "x", core.SearchOptions{Max: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("want 1 result (max=1), got %d", len(results))
	}
}
