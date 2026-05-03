---
name: socialfetch-ledger
description: Local content + seen-ledger for the socialfetch family. Stores every fetched URL (and any URL the agent records via Claude WebFetch / research tools) in a SQLite + FTS5 store + a markdown mirror tree. Use to answer "have we seen this URL?" / "what did we save about X?" / "store this WebFetch output for next time" — before re-fetching, before re-WebFetching, and after any external content fetch the agent wants to remember.
allowed-tools: |
  Bash(scripts/socialfetch-ledger seen *)
  Bash(scripts/socialfetch-ledger get *)
  Bash(scripts/socialfetch-ledger list)
  Bash(scripts/socialfetch-ledger list *)
  Bash(scripts/socialfetch-ledger search *)
  Bash(scripts/socialfetch-ledger stats)
  Bash(scripts/socialfetch-ledger record *)
  Bash(scripts/socialfetch-ledger forget *)
  Bash(scripts/socialfetch-ledger filter *)
  Bash(scripts/socialfetch-ledger help *)
  Bash(scripts/socialfetch-ledger version)
---

# socialfetch-ledger skill

Wraps the `socialfetch-ledger` Go binary at `scripts/socialfetch-ledger`.
The ledger is a **persistent local memory** for every URL the agent
has read — automatic when content comes through `socialfetch fetch`
/ `research` / `timeline`, manual via `record` for content the
agent fetched through other tools (Claude's WebFetch, the research
tool's web search, a curl one-off).

**Trust the ledger.** When the user asks "have we read this
before?" or "what did we learn about X recently?" — query the
ledger first. Only re-fetch on cache miss. The cache survives
across conversations, computer restarts, and Claude versions.

## Subcommands

```
scripts/socialfetch-ledger seen <url>...                # is URL(s) in the ledger?
scripts/socialfetch-ledger get <url>                    # full content of one entry
scripts/socialfetch-ledger list [flags]                 # browse newest first
scripts/socialfetch-ledger search "<terms>"             # FTS5 over title/content
scripts/socialfetch-ledger stats                        # counts, sizes
scripts/socialfetch-ledger record <url> [flags]         # store URL+content (stdin)
scripts/socialfetch-ledger forget <url>                 # drop one entry
scripts/socialfetch-ledger filter --skip-seen           # JSONL passthrough drops seen
```

`scripts/socialfetch-ledger help <subcommand>` for full flag reference.

## Decision rules

**Before fetching anything, ask the ledger first.** If the user
mentions a URL or topic that might already be cached, run `seen` /
`search` before any new fetch:

```bash
scripts/socialfetch-ledger seen "https://news.ycombinator.com/item?id=43000000"
# seen   → use `get` to retrieve, no re-fetch
# unseen → fall through to `socialfetch fetch` or WebFetch
```

For content-bearing queries:

```bash
scripts/socialfetch-ledger search "harness engineering"
# returns BM25-ranked hits across every saved item's title /
# summary / content / author / tags
```

Combine: `search` to find candidates, `get <url>` to dump one in
full, then summarise.

## Recording content from outside socialfetch

When the user asks the agent to fetch content via Claude's
WebFetch tool (or any other non-socialfetch path), the result
isn't in the ledger automatically. Add it after, so the next
conversation finds it:

```bash
# 1. fetch via Claude's WebFetch tool (output: markdown)
# 2. record it:
scripts/socialfetch-ledger record \
  --title "Page Title" \
  --source webfetch \
  "https://example.com/post" <<EOF
# Page Title

Markdown body that came back from WebFetch.
EOF
```

Required: `<url>`, `--title`. Everything else (`--summary`,
`--author`, `--source`, `--canonical-id`, `--content FILE`) is
optional. Source defaults to `webfetch`; override to
`research-tool`, `manual`, etc. for cleaner `list --source`
filtering later.

**Workflow recipe** for the most common case — user asks to read
some URL the agent fetches via WebFetch:

1. `seen <url>` → if seen, skip 2-3, use `get <url>` for the body.
2. WebFetch the URL → capture markdown.
3. `record <url> --title "..." --source webfetch < markdown`.
4. Reason / summarize from the recorded content.

The ledger's deduplication (key = `source::canonical_id`) means
re-recording the same URL with the same content is a no-op
(`unchanged`), and re-recording with new content updates the
existing row — agents can run record liberally without worrying
about duplicates.

## Listing + filtering

```bash
scripts/socialfetch-ledger list                       # newest 25
scripts/socialfetch-ledger list --source hackernews   # only HN entries
scripts/socialfetch-ledger list --source webfetch     # only Claude-recorded
scripts/socialfetch-ledger list --since 7d            # last week
scripts/socialfetch-ledger list -n 100                # bigger window
```

`stats` for an at-a-glance summary:

```bash
scripts/socialfetch-ledger stats
# → counts per source, total items, disk usage, oldest/newest
```

## Storage location

Default: `$XDG_DATA_HOME/socialfetch-ledger` (typically
`~/.local/share/socialfetch-ledger`). Override per-call with
`--data-dir <path>`, or globally with `SOCIALFETCH_LEDGER_DIR`.

The store is a single SQLite file (`ledger.db`) plus an on-disk
markdown mirror tree (`tree/by-source/`, `tree/by-date/`,
`tree/by-url/<host>/`) — agents can also `grep` / `Read` files
under `tree/by-source/` directly for fuzzy "did we save anything
about X" without going through `search`.

## Output format

`list` and `search` emit one item per line:

```
2026-05-03	hackernews	Y Combinator
  https://news.ycombinator.com/item?id=1
```

(date \t source \t title, then a indented URL line). Easy to grep
through and parse. `get` emits full content as markdown with
frontmatter (source, url, author, score, tags, fetched_at). The
JSON output mode for `seen` (`--format json`) is the cleanest
parse target for agent code:

```json
[{"url":"https://...", "seen":true}, {"url":"...", "seen":false}]
```

## When NOT to use this skill

- The user explicitly asks for **fresh** content ("re-fetch X
  with the latest comments"). Hit `socialfetch fetch` directly.
- The ledger is empty or you're certain the URL is new — running
  `seen` first is wasteful, just fetch.
- Bulk delete / migration / restore — out of scope; tell the
  user to operate on the SQLite file directly.

## Related

- `socialfetch fetch` and friends auto-populate the ledger when
  `SOCIALFETCH_LEDGER` is unset/auto/1 and `socialfetch-ledger`
  is on PATH (or bundled alongside in this skill). No env-var
  setup needed for this skill — auto-detect handles it.
- The standalone `socialfetch` skill covers fetch / search /
  ask / timeline / research / bridge. Use both: this skill for
  reading the cache, that one for filling it.
