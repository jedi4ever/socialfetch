// socialfetch-ledger — content + seen-ledger for socialfetch JSONL.
//
// Pipes are the contract. socialfetch produces JSONL of Items; this
// binary ingests, indexes (SQLite + FTS5), mirrors to a markdown
// directory tree for grep-friendly access, and exposes a few
// stream-shaped subcommands so it composes in shell pipelines:
//
//	socialfetch fetch <url> -f jsonl | socialfetch-ledger ingest
//	socialfetch search "..."  -f jsonl | socialfetch-ledger filter --skip-seen | claude ...
//	socialfetch-ledger search "tessl"
//	socialfetch-ledger get <url>
//	socialfetch-ledger list --source hackernews --since 7d
//	socialfetch-ledger stats
//	socialfetch-ledger forget <url>
//	socialfetch-ledger mirror sync
//
// Storage default: $XDG_DATA_HOME/socialfetch-ledger (or
// ~/.local/share/socialfetch-ledger). Override with --data-dir.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// Version moves with the ledger binary, independent of socialfetch.
// Bump on every user-visible change to subcommands, flags, schema,
// or mirror layout.
const Version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "socialfetch-ledger:", err)
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
	case "mirror":
		return cmdMirror(args[1:])
	case "version", "--version", "-v":
		fmt.Println("socialfetch-ledger", Version)
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
// $SOCIALFETCH_LEDGER_DIR (explicit override) and $XDG_DATA_HOME
// (XDG default). Falls back to ~/.local/share/socialfetch-ledger.
func dataDir() (string, error) {
	if d := os.Getenv("SOCIALFETCH_LEDGER_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "socialfetch-ledger"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "socialfetch-ledger"), nil
}

// addCommonFlags registers --data-dir on a FlagSet — every subcommand
// accepts it so users can point a single invocation at an alternate
// ledger (test fixtures, scratch ledger, etc.).
func addCommonFlags(fs *flag.FlagSet, dataDirOut *string) {
	fs.StringVar(dataDirOut, "data-dir", "", "ledger data directory (default: $SOCIALFETCH_LEDGER_DIR or $XDG_DATA_HOME/socialfetch-ledger)")
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
	fmt.Fprintf(w, `socialfetch-ledger %s — content + seen-ledger for socialfetch JSONL

USAGE
  socialfetch-ledger <command> [flags] [args]

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
  mirror sync            reconcile on-disk tree with the store
  mirror rebuild         nuke and recreate the tree from the store
  version                print version
  help                   this message

DATA LOCATION
  Default: $XDG_DATA_HOME/socialfetch-ledger or ~/.local/share/socialfetch-ledger
  Override with --data-dir <path> on any subcommand, or
  set $SOCIALFETCH_LEDGER_DIR.

EXAMPLES
  socialfetch fetch https://news.ycombinator.com/item?id=1 -f jsonl \
    | socialfetch-ledger ingest

  socialfetch search "go 1.27" -f jsonl \
    | socialfetch-ledger filter --skip-seen \
    | jq .

  socialfetch-ledger search "tessl harness"
  socialfetch-ledger list --source hackernews --since 7d
  socialfetch-ledger stats
`, Version)
}
