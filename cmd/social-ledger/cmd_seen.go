package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/ledger/provenance"
	"github.com/jedi4ever/social-skills/internal/ledger/store"
	"github.com/jedi4ever/social-skills/internal/ledger/urlutil"
)

// cmdSeen answers "is this URL already in the ledger?" for one or
// many URLs. Designed for agent workflows that want to skip
// re-fetching content they've already pulled — same intent as
// `filter --skip-seen` but with simpler ergonomics:
//
//	social-ledger article seen <url1> <url2> ...
//	social-ledger article seen -i urls.txt    # one URL per line
//	cat urls.txt | social-ledger article seen # via stdin
//
// Output (default, machine-greppable):
//
//	seen     <url>
//	unseen   <url>
//
// `--format json` emits an array of {"url":"…","seen":bool} objects
// — easier to consume from jq / agent code than parsing the
// space-separated form. `--only seen` / `--only unseen` filter
// the output to just one side, useful for shell loops.
//
// Lookup walks the same candidate-key list as `get`: tries each
// known source's "<source>::<url>" key, then falls back to a
// last-ditch URL scan. That way callers don't need to know which
// source the URL was originally indexed under.
func cmdSeen(args []string) error {
	fs := flag.NewFlagSet("seen", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	input := fs.String("i", "", "read URLs (one per line) from FILE; `-` reads stdin")
	format := fs.String("format", "text", "output format: text or json")
	only := fs.String("only", "", "filter output: empty (both), seen, or unseen")
	if err := fs.Parse(args); err != nil {
		return err
	}

	urls, err := collectSeenURLs(fs.Args(), *input)
	if err != nil {
		return err
	}
	if len(urls) == 0 {
		return fmt.Errorf("seen: no URLs provided (pass as args, -i FILE, or pipe via stdin)")
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

	// result carries everything the agent needs to decide "do I
	// re-fetch?" without a follow-up `get` call. When Seen is true,
	// Source / FetchedAt / Provenance / CanonicalURL are populated
	// from the matched ledger row; otherwise they're empty so JSON
	// consumers see a clean missed-case shape.
	type result struct {
		URL          string `json:"url"`
		Seen         bool   `json:"seen"`
		Source       string `json:"source,omitempty"`
		FetchedAt    string `json:"fetched_at,omitempty"`
		Provenance   string `json:"provenance,omitempty"`
		CanonicalURL string `json:"canonical_url,omitempty"`
	}
	results := make([]result, 0, len(urls))
	for _, u := range urls {
		hit, err := lookupURL(s, u)
		if err != nil {
			return err
		}
		r := result{URL: u, Seen: hit != nil}
		if hit != nil {
			r.Source = hit.Source
			r.FetchedAt = hit.FetchedAt.Format(time.RFC3339)
			r.Provenance = provenance.Classify(hit.Source)
			if hit.URL != "" && hit.URL != u {
				r.CanonicalURL = hit.URL
			}
		}
		results = append(results, r)
	}

	switch strings.ToLower(*only) {
	case "", "all":
		// pass-through
	case "seen":
		filtered := results[:0]
		for _, r := range results {
			if r.Seen {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	case "unseen":
		filtered := results[:0]
		for _, r := range results {
			if !r.Seen {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	default:
		return fmt.Errorf("seen: invalid --only %q (want seen | unseen | all)", *only)
	}

	switch strings.ToLower(*format) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(results)
	case "", "text":
		for _, r := range results {
			if !r.Seen {
				fmt.Printf("%-7s %s\n", "unseen", r.URL)
				continue
			}
			// "seen" line carries source + age inline so an
			// operator scanning text output can spot stale or
			// agent-recorded entries without a follow-up `get`.
			suffix := " (" + r.Source
			if r.Provenance != "" {
				suffix += "/" + r.Provenance
			}
			if r.FetchedAt != "" {
				if t, err := time.Parse(time.RFC3339, r.FetchedAt); err == nil {
					suffix += ", " + humanAge(time.Since(t)) + " ago"
				}
			}
			suffix += ")"
			fmt.Printf("%-7s %s%s\n", "seen", r.URL, suffix)
		}
		return nil
	default:
		return fmt.Errorf("seen: invalid --format %q (want text | json)", *format)
	}
}

// collectSeenURLs gathers URLs from positional args, an -i file,
// and/or piped stdin in priority order. All three sources merge —
// an agent that's mixing flags and a pipe gets the union, with
// duplicates preserved in input order so the caller's expected
// output ordering survives the round-trip.
//
// Empty lines and lines starting with `#` are dropped from file /
// stdin sources, matching the convention of `social-fetch fetch -i`.
func collectSeenURLs(positional []string, inputFile string) ([]string, error) {
	urls := append([]string{}, positional...)

	if inputFile != "" {
		var rd io.Reader
		if inputFile == "-" {
			rd = os.Stdin
		} else {
			f, err := os.Open(inputFile)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			rd = f
		}
		more, err := readURLLines(rd)
		if err != nil {
			return nil, err
		}
		urls = append(urls, more...)
	} else if !isatty(os.Stdin) {
		// Piped input — auto-pick it up, mirroring how `social-fetch
		// fetch` detects a pipe and reads URLs from stdin without
		// requiring -i -.
		more, err := readURLLines(os.Stdin)
		if err != nil {
			return nil, err
		}
		urls = append(urls, more...)
	}
	return urls, nil
}

func readURLLines(r io.Reader) ([]string, error) {
	out := []string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// humanAge formats a duration into the smallest unit that produces
// a number ≥1, so "5m" / "3h" / "2d" / "6w" / "8mo" / "2y" — used
// in the seen text output's "(<source>, <age> ago)" suffix to give
// the operator a quick freshness read without a full timestamp. We
// roll our own rather than pull a date-formatting library because
// the use case is small and English-only.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw", int(d.Hours()/(24*7)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/(24*365)))
	}
}

// isatty reports whether f looks interactive. We avoid a tty
// dependency for a one-place check and rely on os.File.Stat —
// piped stdin is a pipe (mode&ModeCharDevice == 0), interactive
// stdin is a char device.
func isatty(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// lookupURL answers "is this URL in the ledger" + (when found)
// returns the source / fetched_at / canonical url so the seen
// output can carry freshness + provenance without forcing the
// caller to call `get` next. Returns (nil, nil) for a miss.
//
// Search order matches the legacy bool-returning lookupURL:
//
//   - source-prefixed key forms ("hackernews::<url>", "github::<url>", …)
//   - bare URL match against either `url` or `request_url`
//
// Both the supplied form and the normalized form are tried, so a
// `seen` query for `https://Example.com/foo/#anchor` finds an
// entry stored as `https://example.com/foo`. Redirect handling
// (e.g. https://t.co/abc → post-redirect target) works because
// LookupMetaByURL queries both columns and the auto-ingest
// pipeline records both — see internal/core/fetcher.go's
// Registry.Fetch and internal/ledger/store/store.go's HasURL.
func lookupURL(s *store.Store, raw string) (*store.MetaHit, error) {
	candidates := []string{raw}
	if norm := urlutil.Normalize(raw); norm != raw {
		candidates = append(candidates, norm)
	}
	for _, c := range candidates {
		for _, src := range knownSources() {
			hit, err := s.LookupMetaByKey(src + "::" + c)
			if err != nil {
				return nil, err
			}
			if hit != nil {
				return hit, nil
			}
		}
		hit, err := s.LookupMetaByURL(c)
		if err != nil {
			return nil, err
		}
		if hit != nil {
			return hit, nil
		}
	}
	return nil, nil
}
