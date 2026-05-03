// Package item defines the on-the-wire shape of a ledger record.
//
// The shape is deliberately permissive: only the fields the ledger
// queries on are typed (source, url, title, content, fetched_at,
// score, tags). Everything else round-trips through Extra as raw
// JSON, so a social-fetch release that adds a new Item field doesn't
// break ingestion — it just gets preserved verbatim and can be
// queried on later if/when the ledger learns about it.
//
// This package has no dependency on social-fetch. The contract
// between the two binaries is JSON, not Go types.
package item

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Item is the minimal projection of social-fetch's core.Item that the
// ledger needs to index and retrieve. Unknown fields land in Extra
// so they survive a round-trip.
type Item struct {
	Source string `json:"source"`
	Kind   string `json:"kind,omitempty"`

	// URL is the canonical / post-redirect address.
	URL string `json:"url"`

	// RequestURL is the URL the user originally asked for —
	// distinct from URL when a redirect was followed (t.co,
	// bit.ly, 301 redirects). Empty when equal to URL.
	// Indexed separately so `seen` lookups against the
	// user-typed shortener URL match the stored canonical URL.
	RequestURL string `json:"request_url,omitempty"`

	CanonicalID string     `json:"canonical_id,omitempty"`
	Title       string     `json:"title,omitempty"`
	Author      string     `json:"author,omitempty"`
	AuthorURL   string     `json:"author_url,omitempty"`
	Published   *time.Time `json:"published,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Content     string     `json:"content,omitempty"`
	Score       int        `json:"score,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	FetchedAt   time.Time  `json:"fetched_at"`

	// Extra captures every field we don't have a typed slot for.
	// Populated by UnmarshalJSON, written back by MarshalJSON, so a
	// future ledger version can promote fields out of Extra without
	// breaking any persisted JSONL.
	Extra map[string]json.RawMessage `json:"-"`
}

// Key returns the stable primary key the ledger uses to deduplicate.
// Prefers (source, canonical_id) when canonical_id is set, falls back
// to (source, url). Both producers should set canonical_id; the
// fallback exists for tolerance to older or hand-crafted JSONL.
func (it Item) Key() string {
	id := it.CanonicalID
	if id == "" {
		id = it.URL
	}
	return it.Source + "::" + id
}

// Hash returns a SHA-256 of the ledger-relevant fields. Used by the
// mirror layer to detect drift ("file on disk no longer matches the
// row") and by ingest to skip rewriting unchanged items. Order is
// fixed so the hash is stable across ingestions of the same content.
func (it Item) Hash() string {
	h := sha256.New()
	fmt.Fprintln(h, it.Source)
	fmt.Fprintln(h, it.URL)
	fmt.Fprintln(h, it.Title)
	fmt.Fprintln(h, it.Author)
	fmt.Fprintln(h, it.Score)
	fmt.Fprintln(h, strings.Join(it.Tags, ","))
	fmt.Fprintln(h, it.Summary)
	fmt.Fprintln(h, it.Content)
	return hex.EncodeToString(h.Sum(nil))
}

// itemAlias dodges the recursion that would otherwise occur if our
// custom Marshal/Unmarshal called encoding/json on the Item type
// itself.
type itemAlias Item

// UnmarshalJSON populates typed fields and dumps the rest into Extra.
// Both halves are read from the same byte stream — encoding/json
// applies struct tags first, then the second pass into a map captures
// every key (typed or not). We then prune the typed keys from the
// map so Extra contains only the genuinely-unknown fields.
func (it *Item) UnmarshalJSON(data []byte) error {
	var a itemAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*it = Item(a)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Prune the typed fields so Extra is the *complement* of the typed
	// projection, not a duplicate of the whole record.
	for _, k := range typedKeys {
		delete(raw, k)
	}
	if len(raw) > 0 {
		it.Extra = raw
	}
	return nil
}

// MarshalJSON re-emits the typed fields plus everything in Extra. The
// resulting JSON is round-trip stable: marshal → unmarshal → marshal
// produces identical bytes (modulo map key ordering).
func (it Item) MarshalJSON() ([]byte, error) {
	// Serialize the typed half first…
	a := itemAlias(it)
	a.Extra = nil // already excluded by the json:"-" tag, belt-and-suspenders
	typed, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if len(it.Extra) == 0 {
		return typed, nil
	}
	// …then merge Extra in by re-decoding to a map and re-encoding.
	// Inefficient but only runs at write time, not in hot loops.
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(typed, &merged); err != nil {
		return nil, err
	}
	for k, v := range it.Extra {
		// Don't let Extra clobber typed keys — the typed value wins.
		if _, exists := merged[k]; !exists {
			merged[k] = v
		}
	}
	return json.Marshal(merged)
}

// typedKeys lists the JSON field names handled by the typed struct.
// Kept in sync by hand with the struct tags above; tested in
// item_test.go to catch drift.
var typedKeys = []string{
	"source", "kind", "url", "canonical_id", "title", "author",
	"author_url", "published", "summary", "content", "score",
	"tags", "fetched_at",
}
