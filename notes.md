# Design notes — deferred work

Scratch pad for design decisions we've talked through but haven't
implemented yet. Each entry is self-contained so any future session
can pick it up without context-loading the original conversation.

---

## `social-fetch ledger` subcommand + user-level config

**Goal:** make the `SOCIAL_LEDGER*` env vars persistent across
processes without forcing the user to remember an export line each
shell session. Today (`ledger-tool` branch, commit `c2c0ba6`) the
auto-ingest path only fires when env vars are set per-invocation.

**Surface:**

```
social-fetch ledger              # status: enabled? where? which binary?
social-fetch ledger on           # set SOCIAL_LEDGER=1
social-fetch ledger off          # SOCIAL_LEDGER=0 (or remove key)
social-fetch ledger binary <p>   # set SOCIAL_LEDGER_BIN
social-fetch ledger directory <p>  # set SOCIAL_LEDGER_DIR
social-fetch ledger reset        # remove every SOCIAL_LEDGER* key
```

**Storage:** `$XDG_CONFIG_HOME/social-fetch/.env` (default
`~/.config/social-fetch/.env`). Same `KEY=VALUE` shape the existing
dotenv loader already parses — no new format, parser, or serializer
to maintain.

**Precedence (most-specific wins):**
1. Shell env (per-invocation override)
2. Project `./.env` (and parent-dir walk — current behaviour)
3. User config `~/.config/social-fetch/.env` (new, lowest priority)

**Implementation sketch:**
- `internal/ledger/config.go` — atomic read/write of the user
  config (write to `.env.tmp`, fsync, rename). ~40 lines.
- `cmd/social-fetch/ledger.go` — dispatcher for the new subcommand.
  ~80 lines including help text.
- `internal/util/dotenv` — extend `LoadAuto` to also load the
  user config path before parent-dir walk so it's overridable.
  ~10 lines.

**Why hold off:** orthogonal to the ingest hook that just landed on
`ledger-tool`. Better to ship and validate the env-var-driven path
first, layer the subcommand once we know the workflow holds up.

---

## `--prefer-ledger` cache-read path

**Goal:** when SOCIAL_LEDGER_PREFER=1 (or `--prefer-ledger`
flag), check the ledger before hitting the network and return the
cached item if it's not too old. Saves quota on rate-limited APIs
(X v2, Anthropic web_search) and speeds agent loops that revisit
the same URLs.

**Why opt-in (not auto):** stale data surprises. An HN thread that
got 50 new comments since yesterday's fetch wouldn't show them in
auto-cached mode — the most likely user reaction is "this is
broken" before "oh right, the cache". Explicit flag = explicit
behaviour.

**Mechanism:**
- Parent calls `social-ledger get <url> --format json` (the
  ledger's existing `get` subcommand needs a JSON output mode added
  — currently emits human-readable text, see
  `ledge./cmd/social-ledger/cmd_misc.go:90`).
- Unmarshal as `core.Item`, check `fetched_at` against `--max-age`
  (default 24h or per-source TTL — see below).
- Hit: return the item with `Extra.from = "ledger"` and
  `Extra.cache_age_seconds = N` so the agent / consumer can tell
  whether they're seeing fresh or replay data.
- Miss: fall through to the existing network fetch.

**Per-source TTLs (default proposal):**
| Source | TTL | Reason |
|---|---|---|
| `arxiv`, `github` (repo metadata), `youtube` (video meta) | forever (or 30d) | effectively immutable |
| `hackernews`, `reddit`, `twitter`, `linkedin`, `bluesky` | 1h | volatile, comments + scores change |
| `medium`, `substack`, `article` | 7d | articles get edited but rarely |
| anything else | 24h | sane middle ground |

Override via `SOCIAL_LEDGER_MAX_AGE=1h` (global) or per-fetch
`--max-age 30m`.

**Wiring:** same shape as the auto-ingest hook — wrap
`reg.Fetch(ctx, url, opts)` in a helper that does the lookup first
when `ledger.PreferLedger()` is true. Hook in:
- `cmd/social-fetch fetch` (both stream + dir paths)
- `internal/mcp social_fetch_fetch` tool
- `internal/research` angle workers (biggest win — research loops
  hit the same upstream URLs across angles)

**Output marker design (still open):**
- `Extra.from` is a freeform map; agents would need to know to
  inspect it. Cleaner: add a typed `core.Item.CacheHit *CacheHit`
  field with `{Source string, AgeSeconds int64}`. But that's a
  schema change that ripples through the platform packages.
- For v1: stash in `Extra` and document. Promote to typed field
  if we see agents consistently reading from it.

---
