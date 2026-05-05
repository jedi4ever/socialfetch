// social-ledger — content + seen-ledger for social-fetch JSONL.
//
// Pipes are the contract. social-fetch produces JSONL of Items; this
// binary ingests, indexes (SQLite + FTS5), mirrors to a markdown
// directory tree for grep-friendly access, and exposes a few
// stream-shaped subcommands so it composes in shell pipelines:
//
//	social-fetch fetch <url> -f jsonl | social-ledger ingest
//	social-fetch search "..."  -f jsonl | social-ledger filter --skip-seen | claude ...
//	social-ledger search "tessl"
//	social-ledger get <url>
//	social-ledger list --source hackernews --since 7d
//	social-ledger stats
//	social-ledger forget <url>
//	social-ledger mirror sync
//
// Storage default: $XDG_DATA_HOME/social-ledger (or
// ~/.local/share/social-ledger). Override with --data-dir.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Version moves with the ledger binary, independent of social-fetch.
// Bump on every user-visible change to subcommands, flags, schema,
// or mirror layout.
const Version = "0.1.0"

func main() {
	start := time.Now()
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	args := os.Args[2:] // positional args after the subcommand
	err := run(os.Args[1:])
	// Best-effort audit log — never gates the exit code or the
	// error message. See audit.go for path / opt-out details.
	writeAuditLine(cmd, args, start, err)
	if err != nil {
		fmt.Fprintln(os.Stderr, "social-ledger:", err)
		os.Exit(1)
	}
}

// run dispatches the top-level subcommand. Kept small on purpose —
// each subcommand owns its own flag parsing in its dedicated file.
func run(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "ingest":
		return cmdIngest(args[1:])
	case "filter":
		return cmdFilter(args[1:])
	case "search":
		return cmdSearch(args[1:])
	case "get":
		return cmdGet(args[1:])
	case "list":
		return cmdList(args[1:])
	case "stats":
		return cmdStats(args[1:])
	case "forget":
		return cmdForget(args[1:])
	case "seen":
		return cmdSeen(args[1:])
	case "record":
		return cmdRecord(args[1:])
	case "watch":
		return cmdWatch(args[1:])
	case "mirror":
		return cmdMirror(args[1:])
	case "daemon":
		return cmdDaemon(args[1:])
	case "version", "--version", "-v":
		fmt.Println("social-ledger", Version)
		return nil
	case "help", "-h", "--help":
		printHelp(os.Stdout)
		return nil
	default:
		printHelp(os.Stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// dataDir resolves the per-user data directory, honoring
// $SOCIAL_LEDGER_DIR (explicit override) and $XDG_DATA_HOME
// (XDG default). Falls back to ~/.local/share/social-ledger.
func dataDir() (string, error) {
	if d := os.Getenv("SOCIAL_LEDGER_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "social-ledger"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "social-ledger"), nil
}

// addCommonFlags registers --data-dir on a FlagSet — every subcommand
// accepts it so users can point a single invocation at an alternate
// ledger (test fixtures, scratch ledger, etc.).
func addCommonFlags(fs *flag.FlagSet, dataDirOut *string) {
	fs.StringVar(dataDirOut, "data-dir", "", "ledger data directory (default: $SOCIAL_LEDGER_DIR or $XDG_DATA_HOME/social-ledger)")
}

// resolveDataDir picks the explicit --data-dir flag value when set,
// falling back to the env-derived default. Centralized so every
// subcommand's "where's my ledger?" logic is one line.
func resolveDataDir(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	return dataDir()
}

// usageErr is the shape callers return from cmd* functions when the
// arg parsing itself failed. Treated specially in main() so the
// shell exit code (2) matches POSIX convention.
var errUsage = errors.New("usage")

func printHelp(w *os.File) {
	fmt.Fprintf(w, `social-ledger %s — content + seen-ledger for social-fetch JSONL

USAGE
  social-ledger <command> [flags] [args]

COMMANDS
  ingest                 read JSONL from stdin, store + mirror to disk
  filter --skip-seen     pass-through filter that drops already-seen items
  search "<query>"       FTS5 search across stored content
  get <url>              print one stored item (markdown)
  list                   browse items, newest first (-source, -since)
  stats                  counts, sizes, oldest/newest
  forget <url>           drop one item from store + mirror
  seen [<url>...]        check whether URL(s) are in the ledger
                         (URLs from args, -i FILE, or stdin pipe)
  record <url>           store one URL+content pair in the ledger
                         (content on stdin or via --content FILE;
                         use after Claude WebFetch / external curl)
  watch                  tail the ledger audit log and pretty-print
                         events as they happen (--tail N, --since DUR,
                         --raw, --filter)
  mirror sync            reconcile on-disk tree with the store
  mirror rebuild         nuke and recreate the tree from the store
  version                print version
  help                   this message

DATA LOCATION
  Default: $XDG_DATA_HOME/social-ledger or ~/.local/share/social-ledger
  Override with --data-dir <path> on any subcommand, or
  set $SOCIAL_LEDGER_DIR.

EXAMPLES
  social-fetch fetch https://news.ycombinator.com/item?id=1 -f jsonl \
    | social-ledger ingest

  social-fetch search "go 1.27" -f jsonl \
    | social-ledger filter --skip-seen \
    | jq .

  social-ledger search "tessl harness"
  social-ledger list --source hackernews --since 7d
  social-ledger stats
`, Version)
}
