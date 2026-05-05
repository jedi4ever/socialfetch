package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/ledger/item"
	"github.com/jedi4ever/social-skills/internal/ledger/store"
)

// cmdRecord is the agent-friendly entry point for "I fetched
// content from somewhere outside social-fetch (Claude's WebFetch,
// a research tool, a one-off curl) — store it in the ledger
// alongside the rest". Saves the agent from constructing JSONL
// by hand and worrying about field shapes:
//
//	social-ledger article record <url> --title "..." [--summary "..."] \
//	    [--source webfetch] [--author "..."] < /tmp/page.md
//
// Content comes from stdin as raw markdown / text. Title is
// required (an empty title is a near-useless ledger entry —
// nothing to display in `list`, no FTS5 hits via the title
// column). Everything else is optional.
//
// The default --source is "webfetch" since that's the most
// common origin for hand-recorded content; use --source other
// to tag it differently (research, curl, manual, etc.).
//
// Designed so an agent can call it as one shell line after any
// WebFetch invocation, e.g. inside the social-ledger skill:
//
//	social-ledger article record "$URL" --title "$TITLE" < "$CONTENT_FILE"
//
// Failure modes:
//   - missing URL or title → exit 2 (usage)
//   - empty stdin → ingest with empty content (still useful as a
//     "saw this URL" stub; same shape `IngestSources` produces)
//   - storage error → exit 1 with the error on stderr
func cmdRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	var dataDirFlag string
	addCommonFlags(fs, &dataDirFlag)
	source := fs.String("source", "webfetch", "ledger source tag")
	title := fs.String("title", "", "title of the content (required)")
	summary := fs.String("summary", "", "short summary / description")
	author := fs.String("author", "", "author of the content")
	contentFile := fs.String("content", "", "read content from FILE instead of stdin (use `-` for stdin)")
	canonicalID := fs.String("canonical-id", "", "stable cross-platform id (defaults to URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("record: <url> required")
	}
	url := fs.Arg(0)
	if strings.TrimSpace(*title) == "" {
		return fmt.Errorf("record: --title is required (empty titles are useless in the ledger's list / search output)")
	}

	content, err := readRecordContent(*contentFile)
	if err != nil {
		return fmt.Errorf("record: read content: %w", err)
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

	id := *canonicalID
	if id == "" {
		id = url
	}
	now := time.Now().UTC()
	it := item.Item{
		Source:      *source,
		URL:         url,
		CanonicalID: id,
		Title:       *title,
		Summary:     *summary,
		Author:      *author,
		Content:     content,
		FetchedAt:   now,
	}
	res, err := s.Ingest(it)
	if err != nil {
		return fmt.Errorf("record: ingest: %w", err)
	}
	switch res {
	case store.IngestNew:
		fmt.Fprintf(os.Stderr, "recorded: %s (new)\n", url)
	case store.IngestUpdated:
		fmt.Fprintf(os.Stderr, "recorded: %s (updated)\n", url)
	case store.IngestUnchanged:
		fmt.Fprintf(os.Stderr, "recorded: %s (unchanged — same content_hash)\n", url)
	}
	return nil
}

// readRecordContent loads the body bytes from a file (when
// --content FILE is given), stdin (when --content is empty or
// "-"), or returns "" when stdin is a terminal (i.e. nobody
// piped anything in). The terminal-detection avoids a UX hang
// where `record <url> --title …` with no content blocks waiting
// for stdin EOF.
func readRecordContent(file string) (string, error) {
	switch file {
	case "":
		// Auto-pick stdin only when piped — otherwise treat as
		// "no content supplied" so the command doesn't hang on
		// an interactive terminal.
		if isatty(os.Stdin) {
			return "", nil
		}
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	case "-":
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	default:
		b, err := os.ReadFile(file)
		return string(b), err
	}
}
