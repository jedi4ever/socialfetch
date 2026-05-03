package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jedi4ever/socialfetch-ledger/internal/item"
	"github.com/jedi4ever/socialfetch-ledger/internal/mirror"
	"github.com/jedi4ever/socialfetch-ledger/internal/store"
)

// ----- get: print one stored item ---------------------------------

// cmdGet prints one item by URL or canonical id. We try URL first
// (the common case from `socialfetch fetch <url>` output), then
// fall back to a (source, canonical_id) lookup.
func cmdGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	source := fs.String("source", "", "force a specific source when looking up by canonical_id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("get: <url-or-id> required")
	}
	target := fs.Arg(0)

	dir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}
	s, err := store.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		return err
	}
	defer s.Close()

	// Try every candidate Key shape so users can paste either form.
	candidates := []string{}
	if *source != "" {
		candidates = append(candidates, *source+"::"+target)
	}
	// URL-keyed (no source prefix unless we know it from the URL host
	// — but the store's Key derives from source, which the user may
	// not have given. So we scan for url match below as a fallback.
	for _, src := range knownSources() {
		candidates = append(candidates, src+"::"+target)
	}
	for _, k := range candidates {
		it, err := s.Get(k)
		if err != nil {
			return err
		}
		if it != nil {
			printItem(*it)
			return nil
		}
	}
	// Last-ditch: list scan filtered by url. Cheap because URL fits
	// the same index path as a key lookup once the row is found.
	hits, err := s.List(store.ListOpts{Limit: 1000})
	if err != nil {
		return err
	}
	for _, it := range hits {
		if it.URL == target || it.CanonicalID == target {
			printItem(it)
			return nil
		}
	}
	return fmt.Errorf("get: %q not found in ledger", target)
}

// knownSources is the small fixed list of sources we'll try when the
// user gives a bare canonical_id without --source. Kept narrow on
// purpose — adding every possible source would make accidental
// collisions more likely. New socialfetch sources should be added
// here when ledger learns about them.
func knownSources() []string {
	return []string{"hackernews", "reddit", "github", "twitter", "linkedin",
		"medium", "substack", "bluesky", "arxiv", "youtube", "rss", "article"}
}

func printItem(it item.Item) {
	if it.Title != "" {
		fmt.Printf("# %s\n\n", it.Title)
	}
	fmt.Printf("source: %s\n", it.Source)
	fmt.Printf("url: %s\n", it.URL)
	if it.Author != "" {
		fmt.Printf("author: %s\n", it.Author)
	}
	if it.Score != 0 {
		fmt.Printf("score: %d\n", it.Score)
	}
	if it.Published != nil {
		fmt.Printf("published: %s\n", it.Published.UTC().Format(time.RFC3339))
	}
	fmt.Println()
	if it.Summary != "" {
		fmt.Println(it.Summary)
		fmt.Println()
	}
	if it.Content != "" {
		fmt.Println(it.Content)
	}
}

// ----- list: browse ----------------------------------------------

// cmdList browses items newest-first with optional source/since
// filters. The agent flow is "list everything I read about HN this
// week" → eyeball titles → use `get` to dump the body of the
// interesting one.
func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	source := fs.String("source", "", "filter to one source")
	sinceFlag := fs.String("since", "", "only items seen since this duration ago (e.g. 7d, 24h, 2026-04-01)")
	limit := fs.Int("n", 50, "max items")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}
	s, err := store.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		return err
	}
	defer s.Close()

	opts := store.ListOpts{Source: *source, Limit: *limit}
	if *sinceFlag != "" {
		t, err := parseSince(*sinceFlag)
		if err != nil {
			return fmt.Errorf("list: --since: %w", err)
		}
		opts.Since = t
	}
	items, err := s.List(opts)
	if err != nil {
		return err
	}
	for _, it := range items {
		fmt.Printf("%s\t%s\t%s\n",
			it.FetchedAt.UTC().Format("2006-01-02"),
			it.Source,
			truncate(it.Title, 80))
		fmt.Printf("  %s\n", it.URL)
	}
	fmt.Fprintf(os.Stderr, "list: %d item(s)\n", len(items))
	return nil
}

// parseSince accepts both Go duration syntax ("7d" via custom day
// handling, "24h", "30m") and ISO dates ("2026-04-01"). Returns a
// time.Time that List can compare against last_seen_at.
func parseSince(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	// Custom "Nd" handling — time.ParseDuration doesn't know about days.
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err == nil {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("can't parse %q as duration or YYYY-MM-DD", s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ----- stats -----------------------------------------------------

// cmdStats surfaces the numbers a user actually wants when deciding
// "should I prune?" — counts, disk size, oldest and newest items.
// Layout matches `df -h` style: vertical alignment, units inline.
func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}
	s, err := store.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		return err
	}
	defer s.Close()

	st, err := s.Stats()
	if err != nil {
		return err
	}
	fmt.Printf("data dir:  %s\n", dir)
	fmt.Printf("total:     %d items\n", st.Total)
	fmt.Printf("pending:   %d (mirror not yet written)\n", st.Pending)
	fmt.Printf("disk:      %s (SQLite)\n", humanBytes(st.BytesOnDisk))
	if st.Total > 0 {
		fmt.Printf("oldest:    %s\n", st.OldestSeen.Format(time.RFC3339))
		fmt.Printf("newest:    %s\n", st.NewestSeen.Format(time.RFC3339))
	}
	if len(st.BySource) > 0 {
		fmt.Println("by source:")
		for src, n := range st.BySource {
			fmt.Printf("  %-12s %d\n", src, n)
		}
	}
	return nil
}

func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%dB", n)
	case n < k*k:
		return fmt.Sprintf("%.1fKB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1fMB", float64(n)/k/k)
	default:
		return fmt.Sprintf("%.1fGB", float64(n)/k/k/k)
	}
}

// ----- forget ----------------------------------------------------

// cmdForget drops one item from the store and removes its mirror
// files. Idempotent — calling it twice or on a missing item is a
// no-op (with a stderr note so the user knows).
func cmdForget(args []string) error {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	source := fs.String("source", "", "source for the canonical_id form")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("forget: <url-or-id> required")
	}
	target := fs.Arg(0)

	dir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}
	s, err := store.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	m := &mirror.Mirror{Root: filepath.Join(dir, "tree")}

	// Find the item first so we can remove its mirror files even if
	// the canonical Key form isn't obvious from the user's input.
	var found *item.Item
	for _, src := range allSearchSources(*source) {
		if it, err := s.Get(src + "::" + target); err == nil && it != nil {
			it.Source = src
			found = it
			break
		}
	}
	if found == nil {
		// Last-ditch URL scan, mirroring cmdGet's fallback.
		hits, err := s.List(store.ListOpts{Limit: 1000})
		if err != nil {
			return err
		}
		for _, it := range hits {
			if it.URL == target || it.CanonicalID == target {
				found = &it
				break
			}
		}
	}
	if found == nil {
		fmt.Fprintf(os.Stderr, "forget: %q not found (no-op)\n", target)
		return nil
	}
	deleted, err := s.Forget(found.Key())
	if err != nil {
		return err
	}
	if !deleted {
		fmt.Fprintf(os.Stderr, "forget: %q already gone (no-op)\n", target)
		return nil
	}
	if err := m.Remove(*found); err != nil {
		// Mirror cleanup failure is non-fatal — the row is gone, so
		// the orphan-file will be swept on the next `mirror sync`.
		fmt.Fprintf(os.Stderr, "forget: mirror cleanup failed (will be swept on next sync): %v\n", err)
	}
	fmt.Printf("forgot %s\n", found.Key())
	return nil
}

// allSearchSources returns the source list to try for ambiguous
// inputs. When --source is set we trust the user; otherwise we scan
// the same fixed list as cmdGet.
func allSearchSources(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	return knownSources()
}

// ----- mirror -----------------------------------------------------

// cmdMirror handles the `mirror` subcommand group:
//
//	mirror sync     — reconcile on-disk tree against the store
//	mirror rebuild  — nuke the tree and recreate from the store
//
// Sync is the routine path; rebuild is the escape hatch when a user
// has lost trust in the mirror state. Both are O(n) over store rows.
func cmdMirror(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("mirror: subcommand required (sync|rebuild)")
	}
	switch args[0] {
	case "sync":
		return cmdMirrorSync(args[1:])
	case "rebuild":
		return cmdMirrorRebuild(args[1:])
	default:
		return fmt.Errorf("mirror: unknown subcommand %q", args[0])
	}
}

func cmdMirrorSync(args []string) error {
	fs := flag.NewFlagSet("mirror sync", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	dryRun := fs.Bool("dry-run", false, "report what would change without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}
	s, err := store.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	m := &mirror.Mirror{Root: filepath.Join(dir, "tree")}

	// Build the want-set of canonical paths from the store. Pending
	// items get re-written; mirrored items stay put unless the file
	// is missing, in which case we re-write.
	all, err := s.List(store.ListOpts{Limit: 1_000_000})
	if err != nil {
		return err
	}
	want := map[string]bool{}
	var written, errors int
	for _, it := range all {
		path := m.Path(it)
		want[path] = true
		full := filepath.Join(m.Root, path)
		if _, err := os.Stat(full); err == nil {
			continue // canonical file exists; skip
		}
		if *dryRun {
			fmt.Printf("would write: %s\n", path)
			continue
		}
		if rel, err := m.Write(it); err != nil {
			fmt.Fprintf(os.Stderr, "mirror sync: write %s: %v\n", path, err)
			errors++
		} else {
			_ = s.MarkMirrored(it.Key(), rel)
			written++
		}
	}

	// Orphan cleanup pass.
	if *dryRun {
		fmt.Fprintf(os.Stderr, "mirror sync (dry-run): %d would be written\n", written)
		return nil
	}
	rep, err := m.Sync(want)
	if err != nil {
		return err
	}
	fmt.Printf("mirror sync: %d written, %d orphans removed, %d errors\n",
		written, rep.Removed, errors+len(rep.Errors))
	for _, e := range rep.Errors {
		fmt.Fprintln(os.Stderr, "  ", e)
	}
	return nil
}

func cmdMirrorRebuild(args []string) error {
	fs := flag.NewFlagSet("mirror rebuild", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}
	root := filepath.Join(dir, "tree")
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("rebuild: nuke tree: %w", err)
	}
	// Then run a normal sync — every item is now "missing" so every
	// item gets written.
	return cmdMirrorSync(args)
}
