package twitter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// fakeXSearcher records the query it received and returns canned results.
type fakeXSearcher struct {
	gotQuery string
	gotOpts  core.SearchOptions
	results  []core.SearchResult
}

func (f *fakeXSearcher) Search(_ context.Context, query string, opts core.SearchOptions) ([]core.SearchResult, error) {
	f.gotQuery = query
	f.gotOpts = opts
	return f.results, nil
}

func TestXProviderFetchBuildsFromQuery(t *testing.T) {
	pub := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeXSearcher{
		results: []core.SearchResult{
			{Title: "@swyx", URL: "https://x.com/swyx/status/1", Snippet: "first tweet", Published: &pub},
			{Title: "@swyx", URL: "https://x.com/swyx/status/2", Snippet: "second tweet"},
		},
	}
	p := NewXProvider(fake)
	item, err := p.Fetch(context.Background(), "swyx", core.TimelineOptions{Kind: "tweets", Max: 5})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Query must include from:swyx and the kind filter for tweets.
	if !strings.Contains(fake.gotQuery, "from:swyx") {
		t.Errorf("query missing from:swyx: %q", fake.gotQuery)
	}
	if !strings.Contains(fake.gotQuery, "-is:reply") || !strings.Contains(fake.gotQuery, "-is:retweet") {
		t.Errorf("kind=tweets should exclude replies and retweets: %q", fake.gotQuery)
	}
	if fake.gotOpts.After == nil {
		t.Error("expected default After (7d window) when none provided")
	}

	if item.Source != "x" || item.Kind != "timeline" {
		t.Errorf("source/kind = %s/%s", item.Source, item.Kind)
	}
	if item.Author != "swyx" || item.Title != "Timeline of @swyx" {
		t.Errorf("author/title: %q / %q", item.Author, item.Title)
	}
	if len(item.Children) != 2 {
		t.Fatalf("want 2 children, got %d", len(item.Children))
	}
	if item.Children[0].Published == nil || !item.Children[0].Published.Equal(pub) {
		t.Errorf("first child Published not propagated: %+v", item.Children[0].Published)
	}
}

func TestXProviderUnknownKind(t *testing.T) {
	p := NewXProvider(&fakeXSearcher{})
	_, err := p.Fetch(context.Background(), "swyx", core.TimelineOptions{Kind: "garbage"})
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("expected unknown-kind error, got %v", err)
	}
}

func TestXProviderRejectsEmptyUser(t *testing.T) {
	p := NewXProvider(&fakeXSearcher{})
	_, err := p.Fetch(context.Background(), "", core.TimelineOptions{})
	if err == nil {
		t.Error("expected error for empty user")
	}
}
