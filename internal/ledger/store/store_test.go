package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/ledger/item"
)

// newStore opens a fresh on-disk SQLite in t.TempDir. We don't use
// ":memory:" because modernc.org/sqlite has known quirks with shared
// memory DBs across goroutines, and an on-disk file in TempDir is
// auto-cleaned anyway.
func newStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleItem(id, title, body string) item.Item {
	return item.Item{
		Source:      "hackernews",
		URL:         "https://news.ycombinator.com/item?id=" + id,
		CanonicalID: id,
		Title:       title,
		Content:     body,
		FetchedAt:   time.Now(),
	}
}

// First ingest is "new"; same hash repeats are "unchanged"; mutated
// content is "updated". The state machine is the contract the mirror
// layer relies on, so it has to be ironclad.
func TestIngestStateTransitions(t *testing.T) {
	s := newStore(t)
	it := sampleItem("1", "hello", "first body")

	r, err := s.Ingest(it)
	if err != nil || r != IngestNew {
		t.Fatalf("first ingest: %v / %d, want IngestNew", err, r)
	}
	r, _ = s.Ingest(it)
	if r != IngestUnchanged {
		t.Errorf("repeat ingest = %d, want IngestUnchanged", r)
	}
	it.Content = "different body"
	r, _ = s.Ingest(it)
	if r != IngestUpdated {
		t.Errorf("mutated ingest = %d, want IngestUpdated", r)
	}
}

// FTS5 has to find body content, not just titles. Researchers grep
// for substrings inside the comment trees, not just the post title.
func TestSearchHitsBodyAndTitle(t *testing.T) {
	s := newStore(t)
	_, _ = s.Ingest(sampleItem("1", "Tessl harness landed", "boilerplate"))
	_, _ = s.Ingest(sampleItem("2", "Random other story", "this mentions tessl deep in the body"))

	got, err := s.Search("tessl", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("search returned %d items, want 2 (title hit + body hit)", len(got))
	}
}

// `Has` is the hot path for `filter --skip-seen` against a JSONL
// stream. Cheap, no scan, just an index probe.
func TestHasSkipSeen(t *testing.T) {
	s := newStore(t)
	it := sampleItem("99", "x", "y")
	if has, _ := s.Has(it.Key()); has {
		t.Error("Has returned true before ingest")
	}
	_, _ = s.Ingest(it)
	if has, _ := s.Has(it.Key()); !has {
		t.Error("Has returned false after ingest")
	}
}

// Forget removes the row and lets a future ingest treat the item as
// new. Important for the `forget --url X` UX: users expect the item
// to come back in fresh through search after re-fetching.
func TestForgetRoundTrip(t *testing.T) {
	s := newStore(t)
	it := sampleItem("33", "x", "y")
	_, _ = s.Ingest(it)
	deleted, err := s.Forget(it.Key())
	if err != nil || !deleted {
		t.Fatalf("forget: %v / %v", deleted, err)
	}
	r, _ := s.Ingest(it)
	if r != IngestNew {
		t.Errorf("re-ingest after forget = %d, want IngestNew", r)
	}
}

// List's source + since filters compose. Researchers want
// "everything from hackernews in the last 7 days" cheaply.
func TestListFilters(t *testing.T) {
	s := newStore(t)
	_, _ = s.Ingest(sampleItem("a", "hn one", ""))
	_, _ = s.Ingest(sampleItem("b", "hn two", ""))
	mediumItem := item.Item{Source: "medium", URL: "u", Title: "x", FetchedAt: time.Now()}
	_, _ = s.Ingest(mediumItem)

	got, err := s.List(ListOpts{Source: "hackernews"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("source filter: got %d, want 2", len(got))
	}
	for _, it := range got {
		if it.Source != "hackernews" {
			t.Errorf("source filter leaked: %q", it.Source)
		}
	}

	// Items inserted just above all have last_seen_at = now, so a
	// Since filter set to "now+1s" should return zero.
	got, _ = s.List(ListOpts{Since: time.Now().Add(time.Hour)})
	if len(got) != 0 {
		t.Errorf("since filter: got %d items in the future, want 0", len(got))
	}
}

// The mirror layer relies on PendingMirror returning anything that
// hasn't been MarkMirrored'd yet — and on a content-hash change
// flipping a previously-mirrored item back to pending so the new
// content gets written.
func TestPendingMirrorLifecycle(t *testing.T) {
	s := newStore(t)
	it := sampleItem("p", "title", "body")
	_, _ = s.Ingest(it)

	pending, err := s.PendingMirror()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("after ingest: %d pending, want 1", len(pending))
	}

	if err := s.MarkMirrored(it.Key(), "tree/by-source/hn/p.md"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	pending, _ = s.PendingMirror()
	if len(pending) != 0 {
		t.Errorf("after MarkMirrored: %d pending, want 0", len(pending))
	}

	// Mutating content should re-flag the row pending.
	it.Content = "changed"
	_, _ = s.Ingest(it)
	pending, _ = s.PendingMirror()
	if len(pending) != 1 {
		t.Errorf("after content change: %d pending, want 1", len(pending))
	}
}

// Stats is surfaced to users; it has to be honest about counts and
// not double-count or miss sources.
func TestStats(t *testing.T) {
	s := newStore(t)
	_, _ = s.Ingest(sampleItem("a", "x", ""))
	_, _ = s.Ingest(sampleItem("b", "y", ""))
	_, _ = s.Ingest(item.Item{Source: "medium", URL: "u", Title: "z", FetchedAt: time.Now()})

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Total != 3 {
		t.Errorf("total: %d, want 3", st.Total)
	}
	if st.BySource["hackernews"] != 2 || st.BySource["medium"] != 1 {
		t.Errorf("by-source: %v", st.BySource)
	}
	if st.Pending != 3 {
		t.Errorf("pending: %d, want 3", st.Pending)
	}
}

// Extra fields survive a round trip through SQLite's TEXT column.
// This is the schema-drift safety net: if social-fetch adds a new Item
// field, ledger preserves it verbatim and a future ledger version can
// promote it without losing history.
func TestExtraSurvivesRoundTrip(t *testing.T) {
	s := newStore(t)
	src := []byte(`{"source":"hn","url":"u","title":"t","fetched_at":"2026-01-01T00:00:00Z","comment_count":42}`)
	var it item.Item
	if err := json.Unmarshal(src, &it); err != nil {
		t.Fatalf("setup unmarshal: %v", err)
	}
	if _, err := s.Ingest(it); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	got, err := s.Get(it.Key())
	if err != nil || got == nil {
		t.Fatalf("get: %v / %v", got, err)
	}
	if _, ok := got.Extra["comment_count"]; !ok {
		t.Errorf("Extra.comment_count lost across round-trip: %v", got.Extra)
	}
}

// TestIngestNormalizesURL — surface variants of the same URL
// (uppercase host, trailing slash, fragment) collapse to one row.
// Regression guard for the fix that introduced urlutil.Normalize
// on the ingest path.
func TestIngestNormalizesURL(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	variants := []string{
		"https://EXAMPLE.com/post",
		"https://example.com/post/",
		"https://example.com/post#anchor",
		"https://example.com/post",
	}
	for _, u := range variants {
		_, err := s.Ingest(item.Item{
			Source: "article", URL: u, CanonicalID: "post-1",
			Title: "x", Content: "y", FetchedAt: now,
		})
		if err != nil {
			t.Fatalf("ingest %q: %v", u, err)
		}
	}
	stats, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Total != 1 {
		t.Errorf("expected 1 row after 4 normalized ingests, got %d", stats.Total)
	}
}

// TestHasURLMatchesRequestURL — when a fetcher follows a redirect
// (t.co → canonical), HasURL should find the row regardless of
// which URL the caller asks about. This is the core "seen via
// shortener" guarantee — without it, the agent re-fetches the
// same content every time it sees the short link.
func TestHasURLMatchesRequestURL(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	_, err := s.Ingest(item.Item{
		Source:     "article",
		URL:        "https://example.com/canonical",
		RequestURL: "https://t.co/abc",
		Title:      "redirect demo",
		FetchedAt:  now,
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	cases := []struct {
		probe string
		want  bool
		desc  string
	}{
		{"https://example.com/canonical", true, "canonical url column"},
		{"https://t.co/abc", true, "request_url column (redirect short form)"},
		{"https://example.com/different", false, "unrelated url"},
		{"https://t.co/different", false, "unrelated short url"},
	}
	for _, c := range cases {
		got, err := s.HasURL(c.probe)
		if err != nil {
			t.Errorf("%s: HasURL err=%v", c.desc, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: HasURL(%q)=%v, want %v", c.desc, c.probe, got, c.want)
		}
	}
}

// TestIngestRequestURLEqualToURLDropsField — when the fetcher's
// canonical URL matches the user's request, we want the
// request_url column to stay NULL so the partial index doesn't
// double-count and JSON output stays clean.
func TestIngestRequestURLEqualToURLDropsField(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	_, err := s.Ingest(item.Item{
		Source:     "hackernews",
		URL:        "https://news.ycombinator.com/item?id=1",
		RequestURL: "https://news.ycombinator.com/item?id=1", // same as URL
		Title:      "no redirect",
		FetchedAt:  now,
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Re-fetching by the exact same URL via either column should hit.
	hit, err := s.HasURL("https://news.ycombinator.com/item?id=1")
	if err != nil || !hit {
		t.Errorf("HasURL miss on stored item, err=%v hit=%v", err, hit)
	}
}
