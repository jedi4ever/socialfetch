package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jedi4ever/social-skills/internal/ledger/store"
)

// cmdSearch runs FTS5 over title/summary/content/author/tags and
// prints matches in BM25-rank order. Format is intentionally short
// (one line per hit + a snippet) so the output composes well with
// `social-fetch fetch` — the typical flow is "find candidate URLs in
// the ledger, then fetch the full thing for citation".
//
// Use `social-ledger article get <url>` to dump one hit in full.
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
	q = sanitizeFTS5Query(q)

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

// sanitizeFTS5Query phrase-quotes any whitespace-separated bareword
// that would confuse the FTS5 query parser — currently anything
// containing `-`, `:`, `.`, or `/`. FTS5 reserves `-` for negation
// and `:` for column-qualifiers, and an unquoted `context-engineering`
// gets parsed as "term `context` followed by negation of `engineering`"
// (or worse, a column lookup) which yields a "no such column" error.
//
// We DON'T touch:
//   - already-quoted phrases ("vercel ai")
//   - FTS5 operators (AND / OR / NOT / NEAR / parens)
//   - bare alphanumeric terms (harness, golang, hackernews)
//   - prefix-matching tokens (harness*)
//
// Net effect for a real-world agent query like
//
//	harness OR context-engineering OR 12-factor
//
// We rewrite to:
//
//	harness OR "context-engineering" OR "12-factor"
//
// which FTS5 happily evaluates as three OR'd phrase searches.
//
// Edge cases:
//   - quotes embedded in the query are passed through verbatim
//     (FTS5's quoting is just `"…"`, no escapes; nested quotes
//     fail at FTS5 level which is the same behaviour as before).
//   - a bareword that contains BOTH operators and special chars
//     (rare; e.g. `foo:bar-baz`) gets fully quoted — same shape
//     a power user would write.
func sanitizeFTS5Query(q string) string {
	// Operators we leave alone (uppercase, FTS5 keywords).
	operators := map[string]bool{
		"AND": true, "OR": true, "NOT": true, "NEAR": true,
	}

	// Walk token-by-token. A "token" here is a maximal run of
	// non-whitespace, except runs that START with `"` are taken
	// up to the matching closing `"` so quoted phrases stay intact.
	var out strings.Builder
	i := 0
	for i < len(q) {
		// Skip whitespace, preserving the separator.
		if q[i] == ' ' || q[i] == '\t' || q[i] == '\n' {
			out.WriteByte(q[i])
			i++
			continue
		}
		// Already-quoted phrase: copy through to closing quote.
		if q[i] == '"' {
			j := i + 1
			for j < len(q) && q[j] != '"' {
				j++
			}
			if j < len(q) {
				j++ // include the closing quote
			}
			out.WriteString(q[i:j])
			i = j
			continue
		}
		// Parens — pass through, FTS5 grouping syntax.
		if q[i] == '(' || q[i] == ')' {
			out.WriteByte(q[i])
			i++
			continue
		}
		// Read a bareword (run of non-whitespace, non-paren, non-quote).
		j := i
		for j < len(q) {
			c := q[j]
			if c == ' ' || c == '\t' || c == '\n' || c == '(' || c == ')' || c == '"' {
				break
			}
			j++
		}
		token := q[i:j]
		i = j
		// Operator? leave bare.
		if operators[strings.ToUpper(token)] {
			out.WriteString(strings.ToUpper(token))
			continue
		}
		// Contains an FTS5-reserved char? phrase-quote it.
		if strings.ContainsAny(token, "-:./") {
			// Strip a trailing `*` so prefix searches still work
			// when the user writes harness-eng* — quote the
			// word, append the * outside.
			star := ""
			if strings.HasSuffix(token, "*") {
				token = strings.TrimSuffix(token, "*")
				star = "*"
			}
			out.WriteByte('"')
			out.WriteString(token)
			out.WriteByte('"')
			out.WriteString(star)
			continue
		}
		// Plain bareword (or `term*` prefix) — pass through.
		out.WriteString(token)
	}
	return out.String()
}
