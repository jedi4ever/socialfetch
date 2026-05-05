package bookmarks

// Unit tests for the FilterOpts.matches behaviour. Pins the rules
// that the CLI + MCP layer rely on:
//
//   - FolderContains is fuzzy substring (case-insensitive)
//   - Folder is exact-path-with-subtree (case-insensitive)
//   - Combinations AND together
//
// The full Lister.List path (which reads Chrome's JSON file from
// disk) isn't covered here; the CLI integration tests would belong
// elsewhere. This file is fixture-free.

import "testing"

func bookmark(folder string) Bookmark {
	return Bookmark{Folder: folder, Title: "t", URL: "https://example.com"}
}

func TestFilterFolderExactMatch(t *testing.T) {
	f := FilterOpts{Folder: "Bookmarks bar/AI"}
	cases := []struct {
		folder string
		want   bool
	}{
		{"Bookmarks bar/AI", true},                // exact
		{"Bookmarks bar/AI/papers", true},         // direct child
		{"Bookmarks bar/AI/agents/harness", true}, // deep descendant
		{"Bookmarks bar/AI Foo", false},           // sibling that starts the same
		{"Bookmarks bar/Other/AI", false},         // same name but different path
		{"AI", false},                             // partial match should NOT count
		{"Bookmarks bar", false},                  // ancestor
		{"Bookmarks bar/Reading list/AI", false},  // unrelated AI elsewhere
	}
	for _, tc := range cases {
		got := f.matches(bookmark(tc.folder))
		if got != tc.want {
			t.Errorf("matches(folder=%q) with --folder=%q: got %v, want %v",
				tc.folder, f.Folder, got, tc.want)
		}
	}
}

func TestFilterFolderCaseInsensitive(t *testing.T) {
	f := FilterOpts{Folder: "BOOKMARKS BAR/ai"}
	if !f.matches(bookmark("Bookmarks bar/AI/papers")) {
		t.Error("Folder match should be case-insensitive")
	}
}

func TestFilterFolderTrailingSlashIgnored(t *testing.T) {
	f := FilterOpts{Folder: "Bookmarks bar/AI/"}
	if !f.matches(bookmark("Bookmarks bar/AI")) {
		t.Error("trailing slash on Folder should be ignored")
	}
	if !f.matches(bookmark("Bookmarks bar/AI/papers")) {
		t.Error("trailing slash on Folder should match subtree")
	}
}

func TestFilterFolderAndFolderContainsAND(t *testing.T) {
	// Both filters apply together: must be in the AI subtree AND
	// the path must contain "papers" anywhere.
	f := FilterOpts{
		Folder:         "Bookmarks bar/AI",
		FolderContains: "papers",
	}
	if !f.matches(bookmark("Bookmarks bar/AI/papers")) {
		t.Error("should match: in AI subtree + path contains 'papers'")
	}
	if f.matches(bookmark("Bookmarks bar/AI/agents")) {
		t.Error("should NOT match: in AI subtree but path lacks 'papers'")
	}
	if f.matches(bookmark("Bookmarks bar/Other/papers")) {
		t.Error("should NOT match: contains 'papers' but not in AI subtree")
	}
}

func TestFilterFolderEmptyMatchesEverything(t *testing.T) {
	// Zero-value Folder = no scope.
	f := FilterOpts{}
	if !f.matches(bookmark("any/path")) {
		t.Error("zero-value FilterOpts should match every bookmark")
	}
}
