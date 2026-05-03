---
name: social-ledger
description: Local content + seen-ledger for the social-fetch family. Stores every fetched URL (and any URL the agent records via Claude WebFetch / research tools) in a SQLite + FTS5 store + a markdown mirror tree. Use to answer "have we seen this URL?" / "what did we save about X?" / "store this WebFetch output for next time" — before re-fetching, before re-WebFetching, and after any external content fetch the agent wants to remember.
allowed-tools: |
  Bash(scripts/social-ledger seen *)
  Bash(scripts/social-ledger get *)
  Bash(scripts/social-ledger list)
  Bash(scripts/social-ledger list *)
  Bash(scripts/social-ledger search *)
  Bash(scripts/social-ledger stats)
  Bash(scripts/social-ledger record *)
  Bash(scripts/social-ledger forget *)
  Bash(scripts/social-ledger filter *)
  Bash(scripts/social-ledger help *)
  Bash(scripts/social-ledger version)
---

# social-ledger skill

Wraps the `social-ledger` Go binary at `scripts/social-ledger`.
The ledger is a **persistent local memory** for every URL the agent
has read — automatic when content comes through `social-fetch fetch`
/ `research` / `timeline`, manual via `record` for content the
agent fetched through other tools (Claude's WebFetch, the research
tool's web search, a curl one-off).

**Trust the ledger.** When the user asks "have we read this
before?" or "what did we learn about X recently?" — query the
ledger first. Only re-fetch on cache miss. The cache survives
across conversations, computer restarts, and Claude versions.

**Provenance — know how much to trust each entry.** The ledger
records *who put each item in*. Two classes:

- **auto-fetched** — entry was ingested by `social-fetch fetch /
  search / ask / timeline / research`. We pulled the URL ourselves,
  ran our own extractor, normalised the markdown. **High trust.**
  The `source` column is one of the platform names: `hackernews`,
  `reddit`, `github`, `x`, `twitter`, `linkedin`, `youtube`,
  `bluesky`, `arxiv`, `medium`, `substack`, `rss`, `article`.

- **agent-recorded** — entry was stored via `social-ledger record`,
  meaning an agent fed in content it got from somewhere else
  (Claude's WebFetch, the research tool, a `curl` one-off, hand
  paste). **Trust depends on what was fed in.** The `source`
  column is one of `webfetch`, `manual`, `research-tool`,
  `citation`.

Quoting from a `webfetch`-source entry is fine for "what does the
page say at a glance"; for high-stakes citations, prefer
`auto-fetched` entries or re-fetch the URL via `social-fetch fetch`
to get a fresh copy through our extractor. The MCP `social_ledger_get`
tool surfaces this as a `provenance` field on every retrieval; the
CLI shows the `source` column on `list` / `search` / `get` so you
can eyeball it.

## Subcommands

```
scripts/social-ledger seen <url>...                # is URL(s) in the ledger?
scripts/social-ledger get <url>                    # full content of one entry
scripts/social-ledger list [flags]                 # browse newest first
scripts/social-ledger search "<terms>"             # FTS5 over title/content
scripts/social-ledger stats                        # counts, sizes
scripts/social-ledger record <url> [flags]         # store URL+content (stdin)
scripts/social-ledger forget <url>                 # drop one entry
scripts/social-ledger filter --skip-seen           # JSONL passthrough drops seen
```

`scripts/social-ledger help <subcommand>` for full flag reference.

## Decision rules

**Before fetching anything, ask the ledger first.** If the user
mentions a URL or topic that might already be cached, run `seen` /
`search` before any new fetch:

```bash
scripts/social-ledger seen "https://news.ycombinator.com/item?id=43000000"
# seen   → use `get` to retrieve, no re-fetch
# unseen → fall through to `social-fetch fetch` or WebFetch
```

For content-bearing queries:

```bash
scripts/social-ledger search "harness engineering"
# returns BM25-ranked hits across every saved item's title /
# summary / content / author / tags
```

Combine: `search` to find candidates, `get <url>` to dump one in
full, then summarise.

## Recording content from outside social-fetch

> **DO NOT call `record` for content fetched via `social-fetch`.**
> `social-fetch fetch / search / ask / timeline / research`
> all auto-ingest into the ledger via the auto-detected
> sibling binary. Calling `record` on top creates a duplicate
> row. The two are mutually exclusive.
>
> **DO call `record` for content fetched outside social-fetch:**
>   - Claude's `WebFetch` tool (the built-in one)
>   - Claude's research / search tool result snippets
>   - Ad-hoc `curl`, `wget`, hand-pasted text
>   - Any source that didn't go through a `social-*` binary

When the user asks the agent to fetch content via one of those
non-social-fetch paths, the result isn't in the ledger
automatically. Add it after, so the next conversation finds it:

```bash
# 1. fetch via Claude's WebFetch tool (output: markdown)
# 2. record it:
scripts/social-ledger record \
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

**Workflow recipe** for the WebFetch case — user asks the agent
to read a URL that the agent fetches via Claude's WebFetch tool
(NOT social-fetch — if you used `social-fetch fetch`, skip the
record step entirely; it's already in the ledger):

1. `seen <url>` → if seen, skip 2-3, use `get <url>` for the body.
2. WebFetch the URL → capture markdown.
3. **Write the markdown to a temp file** (e.g. `/tmp/<slug>.md`)
   using the Write tool. Then `record <url> --title "..." --source
   webfetch --content /tmp/<slug>.md`. **Don't stream the markdown
   through stdin or as a JSON string when it's longer than a
   handful of lines** — file paths avoid bloating the agent's
   token budget and the MCP escape-encoding overhead.
4. Reason / summarize from the recorded content.

**The mirror workflow for the social-fetch case** (read this so
you don't double-record):

1. `seen <url>` → if seen, skip 2, use `get <url>`.
2. `social-fetch fetch <url>` (or search / ask / etc.) — this
   auto-ingests into the ledger; the agent does NOT need to call
   record. Verify with `seen <url>` afterwards if uncertain.
3. Reason / summarize from the fetched content.

The ledger's deduplication (key = `source::canonical_id`) means
re-recording the same URL with the same content is a no-op
(`unchanged`), and re-recording with new content updates the
existing row — agents can run record liberally without worrying
about duplicates.

### Worked example — record the result of a Claude WebFetch

The agent has been asked to summarize a Vercel blog post that
isn't on a platform `social-fetch` natively recognises. It
WebFetches the URL, then records the result so future "have we
seen this?" queries hit cache.

Step 1 — check if the ledger already has it:

```bash
scripts/social-ledger seen "https://vercel.com/blog/agent-skills"
# unseen   https://vercel.com/blog/agent-skills
```

Step 2 — call Claude's WebFetch tool. The agent gets markdown
content back. **Write it to a temp file with the Write tool**
rather than holding the entire body in conversation memory:

```bash
# (pseudocode for the agent's perspective)
# 1. WebFetch("https://vercel.com/blog/agent-skills")
#    →  markdown body returned
# 2. Write tool: /tmp/vercel-agent-skills.md ← markdown body
```

Step 3 — record by file path (NOT by streaming the content
through). The `--content FILE` flag reads the body from disk so
the markdown never has to round-trip through the agent's prompt
or the MCP JSON-escape:

```bash
scripts/social-ledger record \
  --title "Agent Skills on Vercel" \
  --summary "How Vercel ships agent skills via npx skills add" \
  --source webfetch \
  --author "vercel-labs" \
  --content /tmp/vercel-agent-skills.md \
  "https://vercel.com/blog/agent-skills"
# stderr: recorded: https://vercel.com/blog/agent-skills (new)
```

If the body is genuinely tiny (a one-line description, a tweet
quote) and writing-then-reading is overkill, stdin still works:

```bash
echo "Short body inline." | scripts/social-ledger record \
  --title "Quick Note" --source manual "https://example.com/x"
```

But for any real WebFetch output, prefer the file path —
typical blog posts are 5-50 KB of markdown which is wasteful as
a JSON-string argument or piped stdin.

Step 4 — confirm + summarize. The ledger now has the entry; a
second `seen` returns `seen`, and `get` dumps the markdown back
on demand:

```bash
scripts/social-ledger seen "https://vercel.com/blog/agent-skills"
# seen     https://vercel.com/blog/agent-skills

scripts/social-ledger get "https://vercel.com/blog/agent-skills"
# # Agent Skills on Vercel
# source: webfetch
# url: https://vercel.com/blog/agent-skills
# author: vercel-labs
# (...full body...)
```

Subsequent conversations that ask "what did we save about Vercel
agent skills?" will find this entry via `search`:

```bash
scripts/social-ledger search "vercel agent skills"
# webfetch    Agent Skills on Vercel    https://vercel.com/blog/agent-skills
#   How Vercel ships agent skills via npx skills add
```

**One-liner template for the common case** (file-based, recommended):

```bash
# 1. Write tool: /tmp/<slug>.md ← WebFetch markdown
# 2. Record:
scripts/social-ledger record \
  --title "$TITLE" --source webfetch \
  --content /tmp/<slug>.md \
  "$URL"
```

The Write-then-record pattern keeps the markdown body off the
agent's token budget AND off the MCP JSON-escape path. For
multi-page articles this saves thousands of tokens vs. inlining.

## Listing + filtering

```bash
scripts/social-ledger list                       # newest 25
scripts/social-ledger list --source hackernews   # only HN entries
scripts/social-ledger list --source webfetch     # only Claude-recorded
scripts/social-ledger list --since 7d            # last week
scripts/social-ledger list -n 100                # bigger window
```

`stats` for an at-a-glance summary:

```bash
scripts/social-ledger stats
# → counts per source, total items, disk usage, oldest/newest
```

## Storage location

Default: `$XDG_DATA_HOME/social-ledger` (typically
`~/.local/share/social-ledger`). Override per-call with
`--data-dir <path>`, or globally with `SOCIAL_LEDGER_DIR`.

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
  with the latest comments"). Hit `social-fetch fetch` directly.
- The ledger is empty or you're certain the URL is new — running
  `seen` first is wasteful, just fetch.
- Bulk delete / migration / restore — out of scope; tell the
  user to operate on the SQLite file directly.

## Related

- `social-fetch fetch` and friends auto-populate the ledger when
  `SOCIAL_LEDGER` is unset/auto/1 and `social-ledger`
  is on PATH (or bundled alongside in this skill). No env-var
  setup needed for this skill — auto-detect handles it.
- The standalone `social-fetch` skill covers fetch / search /
  ask / timeline / research / bridge. Use both: this skill for
  reading the cache, that one for filling it.
