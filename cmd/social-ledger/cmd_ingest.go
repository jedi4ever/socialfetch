package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jedi4ever/social-skills/internal/ledger/item"
	"github.com/jedi4ever/social-skills/internal/ledger/mirror"
	"github.com/jedi4ever/social-skills/internal/ledger/store"
)

// cmdIngest reads JSONL from stdin and ingests every line into the
// store + mirror. The "happy path" of write-through:
//
//  1. store.Ingest commits the row and flips mirror_status to
//     'pending' (or 'mirrored' unchanged when the hash matches).
//  2. mirror.Write lays down the canonical .md plus the symlinks.
//  3. store.MarkMirrored stamps the row 'mirrored' and records the
//     relative path so Sync can find drift later.
//
// On a crash anywhere between step 1 and step 3 the row stays
// 'pending' and the next start (or `mirror sync`) replays the writes.
func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	verbose := fs.Bool("v", false, "log per-item ingest result to stderr")
	noMirror := fs.Bool("no-mirror", false, "skip the on-disk file tree (DB only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	s, err := store.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		return err
	}
	defer s.Close()

	var m *mirror.Mirror
	if !*noMirror {
		m = &mirror.Mirror{Root: filepath.Join(dir, "tree")}
	}

	scanner := bufio.NewScanner(os.Stdin)
	// JSONL items can carry sizable HTML/markdown bodies; default 64KB
	// scanner buffer trips on real social-fetch output. Bump to 4MB —
	// enough for the largest HN/article payloads we've observed.
	scanner.Buffer(make([]byte, 0, 1<<20), 4<<20)

	var nNew, nUpdated, nUnchanged, nFailed int
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var it item.Item
		if err := json.Unmarshal(line, &it); err != nil {
			nFailed++
			fmt.Fprintf(os.Stderr, "ingest: bad json: %v (line dropped)\n", err)
			continue
		}
		result, err := s.Ingest(it)
		if err != nil {
			nFailed++
			fmt.Fprintf(os.Stderr, "ingest: %v\n", err)
			continue
		}
		if m != nil && result != store.IngestUnchanged {
			rel, err := m.Write(it)
			if err != nil {
				// Row is in DB but mirror failed — leave mirror_status
				// 'pending' so the next sync picks it up.
				fmt.Fprintf(os.Stderr, "ingest: mirror write failed for %s: %v (will retry on sync)\n", it.Key(), err)
			} else if err := s.MarkMirrored(it.Key(), rel); err != nil {
				fmt.Fprintf(os.Stderr, "ingest: mark mirrored: %v\n", err)
			}
		}
		switch result {
		case store.IngestNew:
			nNew++
		case store.IngestUpdated:
			nUpdated++
		case store.IngestUnchanged:
			nUnchanged++
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "ingest %s: %s\n", it.Key(), resultName(result))
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("read stdin: %w", err)
	}

	fmt.Fprintf(os.Stderr, "ingested: %d new, %d updated, %d unchanged, %d failed\n",
		nNew, nUpdated, nUnchanged, nFailed)
	return nil
}

func resultName(r store.IngestResult) string {
	switch r {
	case store.IngestNew:
		return "new"
	case store.IngestUpdated:
		return "updated"
	case store.IngestUnchanged:
		return "unchanged"
	}
	return "?"
}
