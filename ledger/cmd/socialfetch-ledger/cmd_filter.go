package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jedi4ever/socialfetch-ledger/internal/item"
	"github.com/jedi4ever/socialfetch-ledger/internal/store"
)

// cmdFilter is a Unix-style filter: read JSONL on stdin, emit JSONL
// on stdout, dropping items based on the chosen predicate. Today the
// only predicate is --skip-seen ("drop items already in the ledger");
// future ones (--only-new, --since, --source) follow the same shape.
//
// This is the canonical way to wire ledger awareness into another
// tool without that tool needing a SQLite client:
//
//	socialfetch search "X" -f jsonl \
//	  | socialfetch-ledger filter --skip-seen \
//	  | <consumer>
//
// Stats land on stderr ("of 47 items, 12 dropped as seen") so the
// stdout stream stays pure JSONL for the next pipe stage.
func cmdFilter(args []string) error {
	fs := flag.NewFlagSet("filter", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	skipSeen := fs.Bool("skip-seen", false, "drop items already present in the ledger")
	markSeen := fs.Bool("mark-seen", false, "with --skip-seen: also ingest the items that pass the filter (so next time they're skipped too)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*skipSeen {
		return fmt.Errorf("filter: --skip-seen is required (only filter mode today)")
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

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1<<20), 4<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	var nIn, nOut, nDropped int
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		nIn++
		var it item.Item
		if err := json.Unmarshal(line, &it); err != nil {
			// Pass malformed lines through unchanged — better to let
			// the consumer choke than to silently swallow them.
			out.Write(line)
			out.WriteByte('\n')
			nOut++
			continue
		}
		seen, err := s.Has(it.Key())
		if err != nil {
			return err
		}
		if seen {
			nDropped++
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
		nOut++
		if *markSeen {
			if _, err := s.Ingest(it); err != nil {
				fmt.Fprintf(os.Stderr, "filter: mark-seen ingest failed: %v\n", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	fmt.Fprintf(os.Stderr, "filter --skip-seen: %d in, %d out, %d dropped\n", nIn, nOut, nDropped)
	return nil
}
