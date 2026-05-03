// Package mirror writes ledger items to a real on-disk directory
// tree so agents can use Bash/Read/Grep/Glob against them. The DB
// is the source of truth; the tree is a read-optimized view.
//
// Layout:
//
//	root/
//	  by-source/<source>/<YYYY-MM-DD>/<slug>.md     (canonical files)
//	  by-date/<YYYY-MM-DD>/<source>-<slug>.md       (symlink → canonical)
//	  by-url/<url-slug>.md                          (symlink → canonical)
//
// Each canonical file is YAML frontmatter + the rendered Item content
// + (when present) the comment tree. Frontmatter holds source/url/
// fetched_at/first_seen_at/score/tags so `grep --include='*.md'`
// returns useful context.
//
// Drift recovery: see Sync. The file tree is fully rebuildable from
// the store; users who lose trust can run `mirror rebuild` to nuke
// and redo without touching the DB.
package mirror

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jedi4ever/socialfetch/internal/ledger/item"
)

// Mirror writes Items into a directory under Root.
type Mirror struct {
	// Root is the absolute path to the mirror tree (e.g.
	// ~/.local/share/socialfetch-ledger/tree). Created on first
	// write if missing.
	Root string
}

// Path returns the canonical relative path under Root for an Item.
// Stable: the same Item produces the same path on every call, so
// MarkMirrored can record it and Sync can find drift.
func (m *Mirror) Path(it item.Item) string {
	date := it.FetchedAt.UTC().Format("2006-01-02")
	return filepath.Join("by-source", safeSegment(it.Source), date, slug(it)+".md")
}

// Write renders the item and stores it under Path(it). Creates parent
// dirs as needed; uses tmp-then-rename for crash safety so a partial
// write never leaves an inconsistent file. Returns the relative path
// (which the caller passes to Store.MarkMirrored) on success.
func (m *Mirror) Write(it item.Item) (string, error) {
	rel := m.Path(it)
	abs := filepath.Join(m.Root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	body := render(it)
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	if err := m.writeViews(it, rel); err != nil {
		// View symlinks are best-effort — the canonical file already
		// landed. Surface the error so callers can log, but the item
		// is queryable via SQLite regardless.
		return rel, fmt.Errorf("views: %w", err)
	}
	return rel, nil
}

// Remove deletes an item's canonical file plus any view symlinks
// pointing at it. Best-effort on the symlinks; returns the canonical
// file's removal error since that's the one that matters for
// "is this row gone from disk?".
func (m *Mirror) Remove(it item.Item) error {
	rel := m.Path(it)
	abs := filepath.Join(m.Root, rel)
	// Remove view symlinks first so a failed canonical-file removal
	// doesn't leave dangling symlinks pointing at a missing target.
	_ = os.Remove(filepath.Join(m.Root, viewByDate(it)))
	_ = os.Remove(filepath.Join(m.Root, viewByURL(it)))
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writeViews creates the by-date and by-url symlinks pointing at the
// canonical file. Uses relative symlinks so the tree is portable
// (movable, packageable into a tarball).
func (m *Mirror) writeViews(it item.Item, canonicalRel string) error {
	for _, viewRel := range []string{viewByDate(it), viewByURL(it)} {
		viewAbs := filepath.Join(m.Root, viewRel)
		if err := os.MkdirAll(filepath.Dir(viewAbs), 0o755); err != nil {
			return err
		}
		// Compute relative path from the symlink's directory to the
		// canonical file. Relative symlinks survive `mv` of the whole
		// tree; absolute symlinks would break.
		target, err := filepath.Rel(filepath.Dir(viewAbs), filepath.Join(m.Root, canonicalRel))
		if err != nil {
			return err
		}
		_ = os.Remove(viewAbs) // overwrite if exists
		if err := os.Symlink(target, viewAbs); err != nil {
			return fmt.Errorf("symlink %s: %w", viewRel, err)
		}
	}
	return nil
}

func viewByDate(it item.Item) string {
	date := it.FetchedAt.UTC().Format("2006-01-02")
	return filepath.Join("by-date", date, safeSegment(it.Source)+"-"+slug(it)+".md")
}

// viewByURL builds the by-url symlink path. Always suffixes a hash
// of the full URL so distinct URLs that safeSegment-collapse to the
// same readable stub don't collide on disk. Without the suffix:
//
//	https://x.com/foo/bar  → x.com-foo-bar.md
//	https://x.com/foo!bar  → x.com-foo-bar.md   (! → -, COLLIDES)
//	https://x.com/foo?q=1  → x.com-foo.md       (query stripped before normalize)
//
// With the 12-hex-char suffix (48 bits of entropy) collisions become
// astronomically unlikely within a single user's ledger.
//
// Sharded by host so a flat `by-url/` doesn't grow past per-directory
// file limits (ext4 ~10K, APFS soft-warns past ~100K). For mixed-
// source ledgers — HN / Reddit / X / LinkedIn / articles — host
// distribution is broad enough that no single bucket dominates.
// `ls by-url/` lists hostnames, not opaque hex, so the tree stays
// browseable.
//
// The single-source-skew failure mode (e.g. an X-archive build with
// >100K items all under by-url/x.com/) is a known degradation path;
// fix is to add a second hash-shard tier under the host when that
// becomes a real problem.
func viewByURL(it item.Item) string {
	u, err := url.Parse(it.URL)
	host := "unknown"
	path := ""
	if err == nil && u.Host != "" {
		host = u.Host
		path = u.Path
	}
	pathStub := safeSegment(strings.Trim(path, "/"))
	if pathStub == "" {
		pathStub = "root"
	}
	return filepath.Join("by-url", safeSegment(host), pathStub+"-"+shortHash(it.URL)+".md")
}

// slug builds a short, filesystem-safe identifier for an Item.
// Prefers canonical_id (stable across re-fetches), falls back to a
// hash of the URL. Title is appended when present so a developer
// browsing the tree sees a human-readable filename.
func slug(it item.Item) string {
	id := it.CanonicalID
	if id == "" {
		id = shortHash(it.URL)
	}
	if it.Title != "" {
		return safeSegment(id) + "-" + safeSegment(it.Title)
	}
	return safeSegment(id)
}

// safeSegment renders s into a filesystem-safe path segment. Collapses
// non-alphanum runs to single dashes, lowercases, trims to 80 chars
// so even verbose titles don't blow past common filesystem limits.
var unsafeRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeSegment(s string) string {
	s = unsafeRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	s = strings.ToLower(s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6]) // 12 chars — plenty unique for a single user's ledger
}

// render produces the markdown body of one item. Format is
// frontmatter + summary + content; comments and other rich data live
// inside Extra and are pretty-printed when present.
//
// Kept deliberately simple: the canonical content already comes from
// socialfetch as markdown. We only frame it.
func render(it item.Item) string {
	var b strings.Builder
	b.WriteString("---\n")
	writeFM(&b, "source", it.Source)
	writeFM(&b, "url", it.URL)
	writeFM(&b, "title", it.Title)
	writeFM(&b, "author", it.Author)
	if it.Score != 0 {
		fmt.Fprintf(&b, "score: %d\n", it.Score)
	}
	if len(it.Tags) > 0 {
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(it.Tags, ", "))
	}
	if it.Published != nil {
		writeFM(&b, "published", it.Published.UTC().Format("2006-01-02T15:04:05Z"))
	}
	writeFM(&b, "fetched_at", it.FetchedAt.UTC().Format("2006-01-02T15:04:05Z"))
	b.WriteString("---\n\n")
	if it.Title != "" {
		fmt.Fprintf(&b, "# %s\n\n", it.Title)
	}
	if it.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", it.Summary)
	}
	if it.Content != "" {
		b.WriteString(it.Content)
		if !strings.HasSuffix(it.Content, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func writeFM(b *strings.Builder, k, v string) {
	if v == "" {
		return
	}
	// YAML-quote when the value contains chars that would confuse a
	// permissive parser. Cheap and conservative — not a full quoter.
	if strings.ContainsAny(v, ":#'\"\n") {
		fmt.Fprintf(b, "%s: %q\n", k, v)
		return
	}
	fmt.Fprintf(b, "%s: %s\n", k, v)
}

// SyncReport summarizes what `mirror sync` did. Returned to callers
// so the CLI can print a concise human summary.
type SyncReport struct {
	Wrote    int      // canonical files (re)written
	Removed  int      // orphan files unlinked
	Errors   []string // partial failures (path + reason)
}

// Sync reconciles the on-disk tree against `wantPaths` (the set of
// relative paths the store says should exist). Files in the tree that
// aren't in the set are deleted; missing paths are NOT recreated here
// because the mirror needs the Item content to write — see Write.
//
// The orphan-cleanup half is what makes `forget` correct on disk
// even when the process died between row-delete and file-unlink. The
// re-write half is driven by callers that pass us PendingMirror
// items; this function intentionally does only the deletion side so
// it can run without holding a sql.DB handle.
func (m *Mirror) Sync(wantPaths map[string]bool) (SyncReport, error) {
	rep := SyncReport{}
	canonicalRoot := filepath.Join(m.Root, "by-source")
	if _, err := os.Stat(canonicalRoot); os.IsNotExist(err) {
		return rep, nil
	}
	err := filepath.Walk(canonicalRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(m.Root, path)
		if !wantPaths[rel] {
			if err := os.Remove(path); err != nil {
				rep.Errors = append(rep.Errors, path+": "+err.Error())
				return nil
			}
			rep.Removed++
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	// Sort errors so the output is deterministic across runs — helps
	// when comparing report to report.
	sort.Strings(rep.Errors)
	return rep, nil
}
