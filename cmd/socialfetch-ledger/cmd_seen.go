package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jedi4ever/socialfetch/internal/ledger/store"
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

// lookupURL tries every candidate key shape so a "seen" check
// works regardless of which source originally indexed the URL.
// Mirrors the fallback list cmdGet uses; refactored so both
// commands stay in lockstep when knownSources() grows.
//
// We try the URL as-given first, then the normalized form, so
// trivial variants (trailing slash, lowercase host, fragment) hit
// the cache. **Redirects are NOT handled** — if the user asks
// about `https://t.co/abc` but the ledger only knows the
// post-redirect target, this returns false. Tracking
// request-vs-canonical URL pairs is on the roadmap (notes.md).
func lookupURL(s *store.Store, raw string) (bool, error) {
	candidates := []string{raw}
	if norm := normalizeURL(raw); norm != "" && norm != raw {
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

// normalizeURL flattens trivial URL variants — fragment, trailing
// slash, lowercase scheme + host — so `seen` matches "the same
// URL with slightly different surface form". Returns "" when raw
// isn't parseable; caller treats that as "no normalized form to
// also try".
//
// Deliberately conservative: we don't reorder query params, drop
// utm_* trackers, or follow redirects. Those are content-aware
// decisions that should live in a normalization library, not in
// a one-shot lookup helper.
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String()
}
