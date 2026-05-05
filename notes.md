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
- Parent calls `social-ledger article get <url> --format json` (the
  ledger's existing `article get` subcommand needs a JSON output
  mode added — currently emits human-readable text, see
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

## Event-platform search — Luma + Sessionize

Two tech-event platforms worth considering as native providers:

- **Luma (lu.ma)** — event hosting + RSVPs. Public discovery at
  `lu.ma/discover`. Individual event pages are at `lu.ma/<slug>`
  and ship JSON-LD `<script type="application/ld+json">` blocks
  with `Event` schema (name, startDate, endDate, location,
  organizer, eventStatus). No documented public search API.
- **Sessionize (sessionize.com)** — speaker / agenda management.
  Public event pages live at `sessionize.com/<event-slug>/` with
  `/sessions`, `/speakers`, `/agenda` subpaths. No public search
  API; some events expose JSON via `/api/v2/<event-id>/` but the
  IDs aren't discoverable without a referrer.

**Practical now (no new code):** `tavily` and `serpapi` honor the
`--site` filter, so `social-fetch search "AI agents meetup" -p
tavily --site lu.ma` works today and returns dated event URLs
ranked by relevance. Same pattern for Sessionize:
`--site sessionize.com`.

**Native fetcher worth it if:**
- Users frequently search "what events are happening about X
  next month" — pure search-via-tavily misses upcoming events
  that haven't been indexed yet.
- Agents need structured event metadata (start/end times,
  location, speakers list) that Tavily's snippet doesn't surface.

**Sketch:**
- `internal/platforms/luma/fetch.go` — claims `lu.ma/<slug>`
  URLs, parses JSON-LD `Event` schema → `core.Item` with
  `Kind: "event"`, dates in `Published`, location in a new
  `Item.Extra["event"]` block (or a typed field if events become
  a recurring kind across platforms).
- `internal/platforms/luma/search.go` — scrape `lu.ma/discover`
  with the topic as query parameter. Returns upcoming events
  ordered by date. Best-effort; HTML shape will drift.
- Sessionize search is harder — `sessionize.com` has no
  discover page, only event-scoped agenda pages. Probably skip
  search; just add a fetcher for event/session URLs.

**Hints to add (when shipped):** rate-limit caution (both
platforms watch for scraping), `Event` schema field shape,
Tavily `--site` fallback recipe.

**Status:** deferred. Tavily/SerpAPI `--site` filter covers the
common case; native fetcher has clear scope but no urgent demand
yet.

---

## Body-image extraction for Medium / Substack + downloadable media

**Done in v0.10.14:** body-image extraction across LinkedIn,
Medium, Substack, AND the generic article fetcher.
- LinkedIn: per-platform DOM walk in
  `internal/platforms/linkedin/fetch_extract_media.go`. Avatars,
  reaction badges, comment-thread images dropped; post photos +
  video posters kept.
- Medium / Substack / generic article: shared helper at
  `internal/platforms/article/body_images.go` with per-platform
  CDN host matchers (`mediumImageHost`, `substackImageHost`,
  `anyHTTPHost` for the generic case). Hero from BaseFromPage is
  deduped automatically.
- Configurable size threshold via `SOCIAL_FETCH_MIN_IMAGE_SIZE`
  (default 64px). Operators bump it higher to drop thumbnails.
- ~30 unit tests across the four platforms; live tests for each
  verify `len(Media) > 0` against a stable URL.

**Then: media downloading.** When `SOCIAL_FETCH_DOWNLOAD_MEDIA=1`,
fetch each `Item.Media[].URL` to a temp dir / ledger media
subdir; populate `Media.LocalPath`. Caps via
`SOCIAL_FETCH_MEDIA_MAX_BYTES`. LinkedIn images at
`media.licdn.com` are CDN-served and downloadable without auth
(only the page HTML needs the bridge).

**Then: OCR / vision describe.** Patai (~/dev/knowledge/patai/
providers/media/) has reference implementations for three
strategies:
- `claude.py` — Claude vision describes the image
- `florence.py` — Microsoft Florence-2 (local model)
- `ocr.py` — tesseract for pure text extraction
Their LinkedIn extractor inlines OCR output into Content as a
`[Image text]\n...` block. We could surface the same via either
inline (cheap, OCR-on-fetch) or on-demand (expose
`Media.Describe()` that the agent calls when it cares).

For social-skills the right choice is probably **on-demand via a
new tool** — `social_fetch_describe_image` taking a Media URL or
LocalPath and routing through whichever vision/OCR provider is
configured (`MEDIA_DESCRIBER=claude|tesseract|jina-vision`).
Avoids the OCR-on-every-fetch cost and lets agents cherry-pick
which images deserve attention. The agent's own vision
(Claude Code / Claude Desktop) is already a primitive for the
"can it just look at the image" case.

**Sketch of the cumulative API surface when fully built:**
```go
type Media struct {
    URL       string
    Type      string  // "image" / "video" / "video-poster" / "gif"
    Alt       string
    LocalPath string  // populated when DOWNLOAD_MEDIA=1
    Bytes     int     // size hint (helps agent pick small thumbs first)
    Describe  string  // populated by social_fetch_describe_image when called
}
```

---
