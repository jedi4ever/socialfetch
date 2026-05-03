# socialfetch-ledger

**Content + seen-ledger for `socialfetch` JSONL.** Stores everything
you've fetched in SQLite (with FTS5 full-text search), mirrors it to
a markdown directory tree so agents can `grep`/`Read` against it, and
filters JSONL streams to drop already-seen items.

> Separate Go module so it can move to its own repo
> (`jedi4ever/socialfetch-ledger`) without disturbing `socialfetch`'s
> dep tree. The contract between the two binaries is **JSONL**, not
> Go types.

## What problem it solves

You're using `socialfetch` to research something — pulling HN
threads, articles, tweets, search results — across many sessions.
Without a ledger:

- **Repeats:** the research orchestrator calls `search "X"` and the
  same 5 tweets show up that you read last week.
- **No recall:** "what did I read about tessl harness three weeks
  ago?" → no answer except scrolling chat history.
- **Token waste:** Perplexity rewrites the same summary because the
  agent has no memory.

`socialfetch-ledger` is the persistent layer underneath. It's
opt-in (you pipe to it) and stays out of `socialfetch`'s way.

## Install

```bash
make build                  # → ./dist/socialfetch-ledger
make install                # → $GOBIN/socialfetch-ledger
```

Pure-Go SQLite (`modernc.org/sqlite`), no CGO, single static binary.

## Quickstart

```bash
# Pipe a fetch into the ledger
socialfetch fetch https://news.ycombinator.com/item?id=1 -f jsonl \
  | socialfetch-ledger ingest

# Search across what you've stored
socialfetch-ledger search "tessl harness"

# Drop already-seen items from a search before sending to an agent
socialfetch search "go 1.27" -f jsonl \
  | socialfetch-ledger filter --skip-seen \
  | jq .

# Browse recent items
socialfetch-ledger list --source hackernews --since 7d

# Print one item by URL
socialfetch-ledger get https://news.ycombinator.com/item?id=1

# How big is this getting?
socialfetch-ledger stats
```

## Storage layout

Default location: `$XDG_DATA_HOME/socialfetch-ledger` or
`~/.local/share/socialfetch-ledger`. Override with `--data-dir`
on any subcommand or set `SOCIALFETCH_LEDGER_DIR`.

```
~/.local/share/socialfetch-ledger/
├── ledger.db                          # SQLite + FTS5, source of truth
└── tree/                              # mirrored markdown, agent-friendly
    ├── by-source/
    │   └── hackernews/2026-05-03/42-tessl-harness-landed.md
    ├── by-date/
    │   └── 2026-05-03/hackernews-42-tessl-harness-landed.md  → symlink
    └── by-url/
        └── news.ycombinator.com-item.md → symlink
```

The DB is the source of truth; the tree is rebuildable from it
(`socialfetch-ledger mirror rebuild`). Each `.md` file is YAML
frontmatter + the rendered Item content — agent-friendly for
`grep --include='*.md'` workflows.

## Subcommand reference

| Command | What it does |
|---|---|
| `ingest` | Read JSONL on stdin, upsert into store + write mirror. Stats on stderr. |
| `filter --skip-seen` | Pass-through filter, drops items already in the ledger. JSONL in / JSONL out. |
| `search "<query>"` | FTS5 search across title/summary/content/author/tags. BM25-ranked. |
| `get <url-or-id>` | Print one stored item, frontmatter + content. |
| `list [--source X] [--since 7d]` | Browse newest-first. |
| `stats` | Counts, sizes, oldest/newest, by-source breakdown. |
| `forget <url-or-id>` | Remove from store and mirror. |
| `mirror sync [--dry-run]` | Reconcile on-disk tree against the store. |
| `mirror rebuild` | Nuke `tree/` and recreate from the store. |
| `version` / `help` | What it says on the tin. |

All subcommands accept `--data-dir <path>`. **Flags must come before
positional args** (Go's `flag` package stops at the first non-flag
arg) — e.g. `socialfetch-ledger search --data-dir /tmp/x "tessl"`.

## Schema-drift tolerance

`socialfetch` may add fields to its `Item` shape over time. The ledger
unmarshals into a permissive struct: the fields it indexes on are
typed (`source`, `url`, `title`, `content`, `score`, `tags`,
`fetched_at`); everything else round-trips through `Extra` as raw
JSON. A new `socialfetch` field lands in the ledger without a code
change and can be promoted to a typed column whenever the ledger
catches up. Round-trip stability is locked in by `internal/item`'s
test suite.

## Testing

```bash
make test         # offline unit tests
make test-race    # same with -race
```

Coverage:

- `internal/item` — JSON round-trip, key derivation, hash stability.
- `internal/store` — ingest state machine (new/updated/unchanged),
  FTS5 hits on body+title, `Has`, `Forget`, `List` filters,
  `PendingMirror` lifecycle, stats, Extra preservation.
- `internal/mirror` — canonical path determinism, frontmatter render,
  atomic write (no `.tmp` leftovers), idempotent re-write, symlink
  cleanup on `Remove`, orphan removal in `Sync`.

## Design notes

The big-picture design conversation that produced this layout
(seen-ledger vs cache, FUSE vs flat-files, sync strategy) lives in
the parent repo's commit history. Short version:

- **DB is source of truth, file tree is a read-optimized mirror.**
  Agents already have `Bash`/`Read`/`Grep`/`Glob` tools — give them
  real files instead of MCP resources or FUSE.
- **Write-through with `mirror_status` column.** On crash between
  DB commit and file write, row is left `'pending'`; a startup pass
  or `mirror sync` replays.
- **Atomic file replace** (`tmp + rename`) so a partial write never
  leaves an inconsistent file.
- **Symlinks are relative** so the tree is portable (`mv` survives).

## Versioning

`Version` constant lives at the top of
`cmd/socialfetch-ledger/main.go`. Bump on every user-visible change
to subcommands, flags, schema, or mirror layout.

## License

MIT — same as `socialfetch`.
