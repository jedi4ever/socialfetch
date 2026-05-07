// social-ledger — content + seen-ledger for social-fetch JSONL.
//
// Pipes are the contract. social-fetch produces JSONL of Items; this
// binary ingests, indexes (SQLite + FTS5), mirrors to a markdown
// directory tree for grep-friendly access, and exposes a few
// stream-shaped subcommands so it composes in shell pipelines.
//
// CLI shape — entity-first. Operations on stored content items live
// under `article` (article add, article get, article list, …);
// operations on tracked people/companies live under `influencer`;
// utility commands (`watch`, `mirror`, `daemon`) stay top-level
// because they don't operate on a single entity type. Examples:
//
//	social-fetch fetch <url> -f jsonl | social-ledger article add
//	social-fetch search "..."  -f jsonl | social-ledger article filter --skip-seen | claude ...
//	social-ledger article search "tessl"
//	social-ledger article get <url>
//	social-ledger article list --source hackernews --since 7d
//	social-ledger article stats
//	social-ledger article forget <url>
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
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Version is kept in lockstep with social-fetch (and the
// claude-desktop / claude-code / marketplace manifests). The two
// binaries ship as a pair — ingest writes from social-fetch must
// match the schema social-ledger reads — so bumping one bumps
// them all. See CLAUDE.md "Versioning" for the full lockstep set.
const Version = "0.26.0"

func main() {
	// Auto-load .env from the cwd / repo root so SOCIAL_LEDGER_*,
	// MCP_AUTH_TOKEN, etc. flow through to subcommands without
	// the operator having to `source .env` first. Same shape
	// social-agent / social-fetch already use.
	dotenv.LoadAuto()
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
	case "article":
		return cmdArticle(args[1:])
	case "watch":
		return cmdWatch(args[1:])
	case "mirror":
		return cmdMirror(args[1:])
	case "daemon":
		return cmdDaemon(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "influencer", "influencers":
		return cmdInfluencer(args[1:])
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

// DefaultProject is the project name used when SOCIAL_LEDGER_PROJECT
// is unset. Picked as `social_fetch` so the default bucket is
// self-describing for operators browsing the data dir
// (`projects/social_fetch/ledger.db` vs an unhelpful `default`).
const DefaultProject = "social_fetch"

// dataDir resolves the per-user data directory, honoring
// $SOCIAL_LEDGER_DIR (explicit override), $SOCIAL_LEDGER_PROJECT
// (per-project subdir), and $XDG_DATA_HOME (XDG default). Falls
// back to ~/.local/share/social-ledger.
//
// Project semantics: every ledger lives under <base>/projects/<P>/.
// SOCIAL_LEDGER_PROJECT=X picks project X; unset uses
// `social_fetch` (DefaultProject). Operators can keep separate
// ledgers per research context (work, personal, topic-X) without
// juggling SOCIAL_LEDGER_DIR for each.
//
// One-time migration: if a bare <base>/ledger.db exists from a
// pre-projects install, dataDir auto-moves it (and the WAL/SHM
// side files) into projects/social_fetch/ on first resolution.
// Idempotent — re-running after migration is a no-op since the
// bare file no longer exists.
//
// Each project = a separate SQLite store. Want a daemon for
// project X? Run `SOCIAL_LEDGER_PROJECT=X social-ledger daemon
// start --port <unique>`. The daemon serves one project per
// instance; multiple daemons on different ports for parallel
// projects.
func dataDir() (string, error) {
	base, err := baseDataDir()
	if err != nil {
		return "", err
	}
	proj := strings.TrimSpace(os.Getenv("SOCIAL_LEDGER_PROJECT"))
	if proj == "" {
		proj = DefaultProject
	}
	target := filepath.Join(base, "projects", sanitizeProjectName(proj))

	// One-shot migration of pre-projects bare layout. Only fires
	// when the bare DB exists AND the target doesn't, so it's safe
	// to call from every subcommand on every invocation.
	if proj == DefaultProject {
		if err := migrateBareLedger(base, target); err != nil {
			// Don't block on migration failure — the user can
			// move the file by hand. Surface to stderr so it's
			// visible.
			fmt.Fprintf(os.Stderr, "social-ledger: bare→project migration skipped: %v\n", err)
		}
	}
	return target, nil
}

// migrateBareLedger moves <base>/ledger.db (plus -wal, -shm) into
// <target>/ on first call. No-op when there's nothing to migrate
// or when the target already has data.
//
// Why move all three side files: SQLite WAL mode writes pending
// commits to <db>-wal, with shared-memory state in <db>-shm.
// Leaving them behind while moving the main file would corrupt
// the database — SQLite would re-create empty WAL/SHM next to
// the moved main file and lose anything pending.
func migrateBareLedger(base, target string) error {
	bare := filepath.Join(base, "ledger.db")
	if _, err := os.Stat(bare); err != nil {
		return nil // nothing to migrate
	}
	if _, err := os.Stat(filepath.Join(target, "ledger.db")); err == nil {
		// target already populated — leave the bare file alone so
		// the operator can inspect / merge by hand.
		return fmt.Errorf("bare ledger.db at %s but target %s/ledger.db already exists; not overwriting", bare, target)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := bare + suffix
		dst := filepath.Join(target, "ledger.db"+suffix)
		if _, err := os.Stat(src); err != nil {
			continue // -wal / -shm may not exist; that's fine
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("rename %s → %s: %w", src, dst, err)
		}
	}
	fmt.Fprintf(os.Stderr, "social-ledger: migrated bare ledger.db → %s\n", target)
	return nil
}

// baseDataDir is dataDir() without the project suffix — the
// directory that holds projects/ and (for backwards compat) the
// bare ledger.db when no project is set.
func baseDataDir() (string, error) {
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

// sanitizeProjectName accepts the project name as the operator
// typed it but enforces filesystem safety: alnum + dash + underscore
// only. Keeps the path predictable across OSes (no spaces, no
// shell-metas) and avoids accidental path traversal via "../foo".
// Stripped chars are replaced with `-`.
func sanitizeProjectName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "default"
	}
	return out
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
//
// Always ensures the directory exists — auto-creates on first call
// for a fresh project so reads against an empty project don't
// error with "unable to open database file" before the first write.
func resolveDataDir(flagVal string) (string, error) {
	dir := flagVal
	if dir == "" {
		d, err := dataDir()
		if err != nil {
			return "", err
		}
		dir = d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create ledger dir %s: %w", dir, err)
	}
	return dir, nil
}

// usageErr is the shape callers return from cmd* functions when the
// arg parsing itself failed. Treated specially in main() so the
// shell exit code (2) matches POSIX convention.
var errUsage = errors.New("usage")

func printHelp(w *os.File) {
	fmt.Fprintf(w, `social-ledger %s — content + seen-ledger for social-fetch JSONL

USAGE
  social-ledger <command> [subcommand] [flags] [args]

ENTITY COMMANDS
  article <verb>         operations on stored content items (articles,
                         tweets, HN posts, repos, anything fetched).
                         Verbs: add, get, list, search, seen, stats,
                         forget, record, filter. Run
                         'social-ledger article --help' for details.

  influencer <verb>      operations on tracked people/companies +
                         which of their channels you subscribe to.
                         Verbs: add, remove, list, show, subscribe,
                         unsubscribe. Run 'social-ledger influencer
                         --help' for details.

UTILITY COMMANDS
  watch                  tail the ledger audit log and pretty-print
                         events as they happen (--tail N, --since DUR,
                         --raw, --filter)
  mirror sync            reconcile on-disk tree with the store
  mirror rebuild         nuke and recreate the tree from the store
  daemon <verb>          start/stop/status the ledger HTTP daemon
  mcp                    serve the ledger-only MCP surface on stdio
                         (social_ledger_seen / get / search / record / forget
                         / list / stats / read_file). Pass --readonly to
                         refuse record/forget. Use to wire third-party
                         agents to the ledger without the full social-fetch
                         tool surface.

  version                print version
  help                   this message

DATA LOCATION
  Default: $XDG_DATA_HOME/social-ledger or ~/.local/share/social-ledger
  Override with --data-dir <path> on any subcommand, or
  set $SOCIAL_LEDGER_DIR.

EXAMPLES
  social-fetch fetch https://news.ycombinator.com/item?id=1 -f jsonl \
    | social-ledger article add

  social-fetch search "go 1.27" -f jsonl \
    | social-ledger article filter --skip-seen \
    | jq .

  social-ledger article search "tessl harness"
  social-ledger article list --source hackernews --since 7d
  social-ledger article stats

  social-ledger influencer add karpathy --x karpathy --topics ai,research
  social-ledger influencer list --topic ai
`, Version)
}
