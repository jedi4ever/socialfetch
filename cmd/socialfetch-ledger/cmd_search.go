package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jedi4ever/socialfetch/internal/ledger/store"
)

// cmdSearch runs FTS5 over title/summary/content/author/tags and
// prints matches in BM25-rank order. Format is intentionally short
// (one line per hit + a snippet) so the output composes well with
// `socialfetch fetch` — the typical flow is "find candidate URLs in
// the ledger, then fetch the full thing for citation".
//
// Use `socialfetch-ledger get <url>` to dump one hit in full.
func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	limit := fs.Int("n", 25, "max results")
	if err := fs.Parse(args); err != nil {
		return err
	}
	q := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if q == "" {
		return fmt.Errorf("search: empty query (usage: search \"<terms>\")")
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

	hits, err := s.Search(q, *limit)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "search: 0 results")
		return nil
	}
	for _, it := range hits {
		title := it.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Printf("%s\t%s\t%s\n", it.Source, title, it.URL)
		if it.Summary != "" {
			fmt.Printf("  %s\n", trimToOneLine(it.Summary, 200))
		}
	}
	fmt.Fprintf(os.Stderr, "search: %d result(s)\n", len(hits))
	return nil
}

// trimToOneLine collapses whitespace and caps to maxLen so search
// snippets fit on a single terminal line. Helper rather than fmt
// gymnastics so the search command's output stays scannable.
func trimToOneLine(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxLen {
		s = s[:maxLen-1] + "…"
	}
	return s
}
