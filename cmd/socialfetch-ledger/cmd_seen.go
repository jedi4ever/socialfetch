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

	"github.com/jedi4ever/socialfetch/internal/ledger/store"
	"github.com/jedi4ever/socialfetch/internal/ledger/urlutil"
)

// cmdSeen answers "is this URL already in the ledger?" for one or
// many URLs. Designed for agent workflows that want to skip
// re-fetching content they've already pulled — same intent as
// `filter --skip-seen` but with simpler ergonomics:
//
//	socialfetch-ledger seen <url1> <url2> ...
//	socialfetch-ledger seen -i urls.txt          # one URL per line
//	cat urls.txt | socialfetch-ledger seen       # via stdin
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

	type result struct {
		URL  string `json:"url"`
		Seen bool   `json:"seen"`
	}
	results := make([]result, 0, len(urls))
	for _, u := range urls {
		hit, err := lookupURL(s, u)
		if err != nil {
			return err
		}
		results = append(results, result{URL: u, Seen: hit})
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
			label := "unseen"
			if r.Seen {
				label = "seen"
			}
			fmt.Printf("%-7s %s\n", label, r.URL)
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
// stdin sources, matching the convention of `socialfetch fetch -i`.
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
		// Piped input — auto-pick it up, mirroring how `socialfetch
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

// lookupURL answers "is this URL in the ledger" across every
// possible storage shape:
//
//   - source-prefixed key forms ("hackernews::<url>", "github::<url>", …)
//   - bare URL match against either `url` or `request_url`
//
// Both the supplied form and the normalized form are tried, so a
// `seen` query for `https://Example.com/foo/#anchor` finds an
// entry stored as `https://example.com/foo`. Redirect handling
// (e.g. https://t.co/abc → post-redirect target) works because
// HasURL queries both columns and the auto-ingest pipeline now
// records both — see internal/core/fetcher.go's Registry.Fetch
// and internal/ledger/store/store.go's HasURL.
func lookupURL(s *store.Store, raw string) (bool, error) {
	candidates := []string{raw}
	if norm := urlutil.Normalize(raw); norm != raw {
		candidates = append(candidates, norm)
	}
	for _, c := range candidates {
		for _, src := range knownSources() {
			ok, err := s.Has(src + "::" + c)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		ok, err := s.HasURL(c)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
