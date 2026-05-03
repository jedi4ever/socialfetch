package item

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Round-trip locks in the contract: marshal(unmarshal(x)) == x for
// every field, typed or unknown. Drift in struct tags or typedKeys
// surfaces here.
func TestItemRoundTrip(t *testing.T) {
	src := []byte(`{
		"source": "hackernews",
		"kind": "story",
		"url": "https://news.ycombinator.com/item?id=42",
		"canonical_id": "42",
		"title": "Hello",
		"author": "alice",
		"score": 10,
		"tags": ["go", "rust"],
		"fetched_at": "2026-05-03T12:00:00Z",
		"comment_count": 3,
		"unknown_provider_field": {"nested": true}
	}`)

	var it Item
	if err := json.Unmarshal(src, &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if it.Source != "hackernews" || it.Score != 10 || it.Title != "Hello" {
		t.Errorf("typed fields wrong: %+v", it)
	}
	if got, want := len(it.Tags), 2; got != want {
		t.Errorf("tags len = %d, want %d", got, want)
	}
	if _, ok := it.Extra["comment_count"]; !ok {
		t.Errorf("Extra missing comment_count: %v", it.Extra)
	}
	if _, ok := it.Extra["unknown_provider_field"]; !ok {
		t.Errorf("Extra missing unknown_provider_field: %v", it.Extra)
	}
	// Typed keys must NOT leak into Extra.
	if _, leaked := it.Extra["score"]; leaked {
		t.Errorf("typed key 'score' leaked into Extra")
	}

	// JSON whitespace ("{\"nested\": true}" vs "{\"nested\":true}") is
	// normalized on re-encode, so byte-identity with the original
	// input isn't a sensible invariant. What we DO require is
	// idempotence after the first canonicalising pass: marshal →
	// unmarshal → marshal must be byte-stable, and the typed fields
	// must survive every cycle.
	first, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	var mid Item
	if err := json.Unmarshal(first, &mid); err != nil {
		t.Fatalf("unmarshal 2: %v", err)
	}
	second, err := json.Marshal(mid)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("re-marshal not idempotent after canonicalising pass:\n  first:  %s\n  second: %s", first, second)
	}
	// Typed fields must survive the canonicalising pass intact.
	if !reflect.DeepEqual(it.Source, mid.Source) || it.Score != mid.Score ||
		!reflect.DeepEqual(it.Tags, mid.Tags) {
		t.Errorf("typed fields drifted across round-trip:\n  before: %+v\n  after:  %+v", it, mid)
	}
}

// Key prefers canonical_id but falls back to url so the ledger keeps
// working with hand-crafted or older JSONL that doesn't set
// canonical_id.
func TestItemKey(t *testing.T) {
	a := Item{Source: "hn", CanonicalID: "42", URL: "https://x/42"}
	b := Item{Source: "hn", URL: "https://x/42"}
	if a.Key() == b.Key() {
		t.Errorf("canonical_id-keyed and url-keyed items shouldn't collide; got %q", a.Key())
	}
	if a.Key() != "hn::42" {
		t.Errorf("canonical_id key wrong: %q", a.Key())
	}
	if b.Key() != "hn::https://x/42" {
		t.Errorf("url-fallback key wrong: %q", b.Key())
	}
}

// Hash changes when content changes; same content produces same hash.
// The mirror's drift detection depends on this.
func TestItemHashStable(t *testing.T) {
	now := time.Now()
	a := Item{Source: "hn", URL: "u", Title: "t", Content: "body", FetchedAt: now}
	b := a
	if a.Hash() != b.Hash() {
		t.Errorf("equal items hashed differently:\n  a=%s\n  b=%s", a.Hash(), b.Hash())
	}
	b.Content = "different body"
	if a.Hash() == b.Hash() {
		t.Errorf("content change did not affect hash")
	}
	// FetchedAt deliberately NOT in hash — re-fetching the same content
	// shouldn't trigger a mirror rewrite.
	c := a
	c.FetchedAt = now.Add(time.Hour)
	if a.Hash() != c.Hash() {
		t.Errorf("FetchedAt should not affect hash (unchanged content shouldn't rewrite mirror)")
	}
}
