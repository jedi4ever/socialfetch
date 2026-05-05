package influencers

// Unit tests for the pure helpers — slugify, mergeTopics,
// mergeFollows. These pin the invariants that the rest of the
// package (and the MCP / CLI dispatchers above it) depend on:
//
//   - re-running Slugify is idempotent (so the agent passing
//     "Andrej Karpathy" or "andrej-karpathy" lands at the same row);
//   - mergeTopics is sorted-dedup union (so re-adding with a new
//     topic doesn't drop existing ones);
//   - mergeFollows upserts by platform (so re-subscribing on x
//     doesn't create a phantom second x-follow row).
//
// The integration-level tests (Add/Get/List round-trip via a real
// store) live in storage_test.go; this file is fixture-free.

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Andrej Karpathy", "andrej-karpathy"},
		{"andrej-karpathy", "andrej-karpathy"}, // idempotent
		{"  Cole Medin  ", "cole-medin"},       // trim
		{"Vercel", "vercel"},
		{"Jane Q. Doe", "jane-q-doe"},        // punctuation collapses
		{"---weird---name---", "weird-name"}, // collapse + trim
		{"日本語", "default"},                   // non-alnum-only → "default"
		{"", "default"},
		{"X", "x"},
	}
	for _, tc := range cases {
		got := Slugify(tc.in)
		if got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
		// Idempotence: Slugify(Slugify(x)) == Slugify(x)
		if again := Slugify(got); again != got {
			t.Errorf("Slugify not idempotent: Slugify(%q)=%q then Slugify(%q)=%q", tc.in, got, got, again)
		}
	}
}

func TestMergeTopics(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{"empty", nil, nil, []string{}},
		{"a only", []string{"ai", "agents"}, nil, []string{"agents", "ai"}},
		{"b only", nil, []string{"ai", "agents"}, []string{"agents", "ai"}},
		{
			"union sorted-dedup",
			[]string{"ai", "agents"},
			[]string{"agents", "harness"},
			[]string{"agents", "ai", "harness"},
		},
		{
			"case-insensitive dedup, first-seen casing wins",
			[]string{"AI", "Agents"},
			[]string{"ai", "agents", "Harness"},
			[]string{"Agents", "AI", "Harness"},
		},
		{
			"trims whitespace + drops empties",
			[]string{"  ai  ", "", "agents"},
			[]string{"   "},
			[]string{"agents", "ai"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeTopics(tc.a, tc.b)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeTopics(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestMergeFollows(t *testing.T) {
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)

	t.Run("appends when platform new", func(t *testing.T) {
		prior := []Follow{
			{Platform: "x", Topics: []string{"ai"}, Since: t1},
		}
		got := mergeFollows(prior, Follow{
			Platform: "github",
			Topics:   []string{"agents"},
			Since:    t2,
		})
		if len(got) != 2 {
			t.Fatalf("want 2 follows, got %d: %#v", len(got), got)
		}
		// Sorted by platform: github before x.
		if got[0].Platform != "github" || got[1].Platform != "x" {
			t.Errorf("want sorted [github, x], got [%s, %s]", got[0].Platform, got[1].Platform)
		}
	})

	t.Run("merges topics + replaces note when platform exists", func(t *testing.T) {
		prior := []Follow{
			{Platform: "x", Topics: []string{"ai"}, Note: "old note", Since: t1},
		}
		got := mergeFollows(prior, Follow{
			Platform: "x",
			Topics:   []string{"research"},
			Note:     "new note",
			Since:    t2,
		})
		if len(got) != 1 {
			t.Fatalf("want 1 follow (merged), got %d: %#v", len(got), got)
		}
		f := got[0]
		if f.Platform != "x" {
			t.Errorf("platform: want x, got %s", f.Platform)
		}
		// Topics union, sorted-dedup.
		want := []string{"ai", "research"}
		if !reflect.DeepEqual(f.Topics, want) {
			t.Errorf("topics: want %v, got %v", want, f.Topics)
		}
		if f.Note != "new note" {
			t.Errorf("note: want %q, got %q", "new note", f.Note)
		}
		if !f.Since.Equal(t2) {
			t.Errorf("since: want %v, got %v", t2, f.Since)
		}
	})

	t.Run("empty note doesn't clobber existing", func(t *testing.T) {
		prior := []Follow{
			{Platform: "x", Topics: []string{"ai"}, Note: "kept", Since: t1},
		}
		got := mergeFollows(prior, Follow{Platform: "x"})
		if got[0].Note != "kept" {
			t.Errorf("note: want %q (preserved), got %q", "kept", got[0].Note)
		}
	})

	t.Run("zero Since doesn't clobber existing", func(t *testing.T) {
		prior := []Follow{
			{Platform: "x", Since: t1},
		}
		got := mergeFollows(prior, Follow{Platform: "x"})
		if !got[0].Since.Equal(t1) {
			t.Errorf("since: want %v (preserved), got %v", t1, got[0].Since)
		}
	})

	t.Run("case-insensitive platform match", func(t *testing.T) {
		prior := []Follow{
			{Platform: "x", Topics: []string{"ai"}},
		}
		got := mergeFollows(prior, Follow{
			Platform: "X", // upper case, same channel
			Topics:   []string{"research"},
		})
		if len(got) != 1 {
			t.Fatalf("upper/lower X should merge, got %d follows: %#v", len(got), got)
		}
	})
}

func TestFiltersMatchTopicAcrossFollows(t *testing.T) {
	// FilterOpts.matches should hit when the search term shows up
	// in a follow's topics, even if the influencer's general Topics
	// list doesn't mention it. This is the "AI authorities" search
	// returning someone followed-for-AI on X but whose Topics says
	// only ["transformers"].
	inf := &Influencer{
		Slug:   "x",
		Name:   "Test",
		Type:   "person",
		Topics: []string{"transformers"},
		Follows: []Follow{
			{Platform: "x", Topics: []string{"ai"}},
		},
	}
	if !(FilterOpts{Topic: "ai"}).matches(inf) {
		t.Error("Topic=ai should match via follow.Topics")
	}
	if !(FilterOpts{Topic: "transformers"}).matches(inf) {
		t.Error("Topic=transformers should match via .Topics")
	}
	if (FilterOpts{Topic: "blockchain"}).matches(inf) {
		t.Error("Topic=blockchain should NOT match")
	}
}

func TestFiltersHasPlatform(t *testing.T) {
	inf := &Influencer{
		Slug:    "x",
		Name:    "T",
		Socials: map[string]string{"x": "@t", "linkedin": ""},
	}
	if !(FilterOpts{HasPlatform: "x"}).matches(inf) {
		t.Error("HasPlatform=x should match")
	}
	// Empty handle should NOT count as "has".
	if (FilterOpts{HasPlatform: "linkedin"}).matches(inf) {
		t.Error("HasPlatform=linkedin should NOT match (empty handle)")
	}
	// Case-insensitive lookup.
	if !(FilterOpts{HasPlatform: "X"}).matches(inf) {
		t.Error("HasPlatform=X should match (case-insensitive)")
	}
}

func TestFiltersFollowedOnly(t *testing.T) {
	withFollows := &Influencer{Follows: []Follow{{Platform: "x"}}}
	withoutFollows := &Influencer{}
	if !(FilterOpts{FollowedOnly: true}).matches(withFollows) {
		t.Error("FollowedOnly should match influencer with follows")
	}
	if (FilterOpts{FollowedOnly: true}).matches(withoutFollows) {
		t.Error("FollowedOnly should NOT match influencer without follows")
	}
}

func TestSlugifyDoesNotPanicOnControlChars(t *testing.T) {
	// Defensive: slug helper used to receive arbitrary user input
	// from CLI / MCP. Make sure newlines / tabs / nulls don't blow
	// up the dash-collapse loop or produce a slug containing them.
	cases := []string{"name\nwith\nnewlines", "name\twith\ttabs", "name\x00with\x00nulls"}
	for _, in := range cases {
		got := Slugify(in)
		if strings.ContainsAny(got, "\n\t\x00") {
			t.Errorf("Slugify(%q) = %q — contains control char", in, got)
		}
	}
}
