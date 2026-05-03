package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/socialfetch/internal/ledger/item"
)

func newMirror(t *testing.T) *Mirror {
	t.Helper()
	return &Mirror{Root: t.TempDir()}
}

func makeItem(source, id, title, body string) item.Item {
	return item.Item{
		Source:      source,
		URL:         "https://" + source + ".test/" + id,
		CanonicalID: id,
		Title:       title,
		Content:     body,
		Score:       42,
		Tags:        []string{"go", "rust"},
		FetchedAt:   time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
	}
}

// Write places the canonical file at by-source/<src>/<date>/<slug>.md
// AND creates the by-date and by-url symlinks. All three paths are
// what agents grep against, so all three are part of the contract.
func TestWriteCreatesCanonicalAndViews(t *testing.T) {
	m := newMirror(t)
	it := makeItem("hackernews", "42", "Hello World", "body content here")

	rel, err := m.Write(it)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	abs := filepath.Join(m.Root, rel)
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("canonical missing: %v", err)
	}
	if !strings.Contains(rel, "by-source/hackernews/2026-05-03/") {
		t.Errorf("canonical path missing source/date: %q", rel)
	}

	// by-date symlink resolves to the canonical file
	byDate := filepath.Join(m.Root, "by-date", "2026-05-03")
	entries, _ := os.ReadDir(byDate)
	if len(entries) != 1 {
		t.Fatalf("by-date entries: %d, want 1 (got %v)", len(entries), entries)
	}
	target, err := os.Readlink(filepath.Join(byDate, entries[0].Name()))
	if err != nil {
		t.Fatalf("readlink by-date: %v", err)
	}
	if !strings.Contains(target, "by-source/hackernews") {
		t.Errorf("by-date symlink target wrong: %q", target)
	}

	// by-url symlink exists
	byURL := filepath.Join(m.Root, "by-url")
	urlEntries, _ := os.ReadDir(byURL)
	if len(urlEntries) != 1 {
		t.Fatalf("by-url entries: %d, want 1", len(urlEntries))
	}
}

// Frontmatter is the agent's grep target for "find HN posts about X
// scored > 50" — has to actually contain the values.
func TestRenderedFrontmatter(t *testing.T) {
	m := newMirror(t)
	it := makeItem("hn", "1", "Tessl harness", "the body")
	rel, err := m.Write(it)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(m.Root, rel))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)
	// URLs and ISO timestamps contain `:` so writeFM quotes them. Tags
	// don't, so they stay unquoted. Both forms are checked here so
	// any future change to writeFM that breaks YAML compat surfaces.
	for _, fragment := range []string{
		"---\n",
		"source: hn",
		`url: "https://hn.test/1"`,
		"score: 42",
		"tags: [go, rust]",
		"# Tessl harness",
		"the body",
	} {
		if !strings.Contains(s, fragment) {
			t.Errorf("rendered output missing %q\n--full output--\n%s", fragment, s)
		}
	}
}

// Crash safety: a partial write must not be visible. We can't easily
// induce a crash in a unit test, but we can verify the .tmp file
// pattern by checking that no stray .tmp files exist after a normal
// write — the rename clears it.
func TestWriteIsAtomic(t *testing.T) {
	m := newMirror(t)
	if _, err := m.Write(makeItem("src", "x", "t", "b")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var stray []string
	_ = filepath.Walk(m.Root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".tmp") {
			stray = append(stray, p)
		}
		return nil
	})
	if len(stray) != 0 {
		t.Errorf("stray .tmp files after successful write: %v", stray)
	}
}

// Re-writing the same item produces an identical canonical file — the
// store's Unchanged path relies on this not duplicating data on disk.
func TestWriteIsIdempotent(t *testing.T) {
	m := newMirror(t)
	it := makeItem("src", "x", "t", "b")
	rel, _ := m.Write(it)
	first, _ := os.ReadFile(filepath.Join(m.Root, rel))
	rel2, _ := m.Write(it)
	if rel != rel2 {
		t.Errorf("paths differed across writes: %q vs %q", rel, rel2)
	}
	second, _ := os.ReadFile(filepath.Join(m.Root, rel2))
	if string(first) != string(second) {
		t.Errorf("body differed across re-writes")
	}
}

// Remove deletes the canonical file AND the symlinks. Otherwise
// `find tree/by-date/` would still surface a forgotten item via a
// dangling symlink.
func TestRemoveCleansSymlinks(t *testing.T) {
	m := newMirror(t)
	it := makeItem("src", "x", "t", "b")
	if _, err := m.Write(it); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m.Remove(it); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Walk the tree and assert no .md files remain.
	var found []string
	_ = filepath.Walk(m.Root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".md") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf(".md files survived Remove: %v", found)
	}
}

// Sync's orphan-cleanup is what makes `forget` correct after a
// crash mid-operation. Set up a tree with a file the store doesn't
// know about and confirm Sync removes it.
func TestSyncRemovesOrphans(t *testing.T) {
	m := newMirror(t)
	good := makeItem("src", "1", "t", "b")
	orphan := makeItem("src", "2", "t", "b")
	_, _ = m.Write(good)
	_, _ = m.Write(orphan)

	// Tell Sync only `good` should exist on disk.
	want := map[string]bool{m.Path(good): true}
	rep, err := m.Sync(want)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if rep.Removed != 1 {
		t.Errorf("removed: %d, want 1", rep.Removed)
	}
	// Verify good still exists, orphan doesn't.
	if _, err := os.Stat(filepath.Join(m.Root, m.Path(good))); err != nil {
		t.Errorf("good item removed by mistake: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, m.Path(orphan))); !os.IsNotExist(err) {
		t.Errorf("orphan survived Sync: err=%v", err)
	}
}

// Path is the contract between store.MarkMirrored and the on-disk
// layout. It must be deterministic — same Item, same path — so Sync's
// drift detection can rely on it. Two independently-constructed Items
// keep the linter from flagging this as comparing-X-to-X.
func TestPathDeterministic(t *testing.T) {
	m := newMirror(t)
	a := m.Path(makeItem("hn", "1", "x", "y"))
	b := m.Path(makeItem("hn", "1", "x", "y"))
	if a != b {
		t.Errorf("Path is non-deterministic: %s vs %s", a, b)
	}
}

// TestViewByURLNoCollision is the regression guard for the bug where
// distinct URLs that safeSegment-collapse to the same readable stub
// (e.g. /foo/bar vs /foo!bar vs /foo?q=bar) all wrote to the same
// by-url symlink path, with the second ingest silently overwriting
// the first. The hash suffix on viewByURL guarantees the paths
// differ even when the human-readable portion is identical.
func TestViewByURLNoCollision(t *testing.T) {
	urls := []string{
		"https://x.com/foo/bar",
		"https://x.com/foo!bar",
		"https://x.com/foo?q=1",
		"https://x.com/foo",
		"https://x.com/foo/bar/", // trailing slash variant
	}
	seen := map[string]string{}
	for _, u := range urls {
		it := item.Item{Source: "x", URL: u, FetchedAt: time.Now()}
		got := viewByURL(it)
		if prev, ok := seen[got]; ok {
			t.Errorf("collision: %q and %q both produce %s", prev, u, got)
		}
		seen[got] = u
	}
}

// TestViewByURLDeterministic — same URL, same path, every time.
// The hash suffix shouldn't randomize across calls. Constructing two
// independent Items (rather than calling viewByURL twice on one
// variable) keeps the linter from flagging this as comparing-X-to-X.
func TestViewByURLDeterministic(t *testing.T) {
	a := viewByURL(item.Item{Source: "x", URL: "https://example.com/abc"})
	b := viewByURL(item.Item{Source: "x", URL: "https://example.com/abc"})
	if a != b {
		t.Errorf("non-deterministic: %s vs %s", a, b)
	}
}

// TestViewByURLEmptyHost covers the malformed-URL path. We should
// fall back to "url-<hash>.md" rather than something traversable.
func TestViewByURLEmptyHost(t *testing.T) {
	it := item.Item{Source: "x", URL: "not-a-url"}
	got := viewByURL(it)
	if !strings.HasPrefix(got, "by-url/") || !strings.HasSuffix(got, ".md") {
		t.Errorf("malformed URL fell back to unexpected path: %s", got)
	}
	// Two malformed URLs must still differ.
	it2 := item.Item{Source: "x", URL: "also-not-a-url"}
	if viewByURL(it) == viewByURL(it2) {
		t.Error("two distinct malformed URLs collapsed to the same path")
	}
}
