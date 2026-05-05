# social-skills — hints & gotchas

Accumulated landmines from real failures. Things the API responses won't
tell you, the docs bury, or the error message points the wrong way.

When a fetch / search / ask comes back wrong and the error doesn't
obviously explain it, scan this file before reaching for `--log -`.

---

## Cloudflare / bot-challenge mirrors

Some sites sit behind Cloudflare with a JS-fingerprinting challenge
enabled. They return **HTTP 403** with header `cf-mitigated: challenge`
to any non-browser client — changing the User-Agent doesn't help; the
challenge wants Chromium TLS fingerprint + client hints + JS execution
to set a `__cf_bm` cookie.

| blocked URL | use instead |
|---|---|
| `platform.openai.com/docs/...` | `developers.openai.com/api/docs/...` |

Likely candidates worth pre-checking before assuming a fetch is broken:
`developer.x.com`, `console.anthropic.com` docs pages.

---

## Hard API tier caps (errors that don't explain themselves)

| provider | cap | symptom | fix |
|---|---|---|---|
| X v2 recent search | 7-day window on `start_time` | HTTP 400, no body explanation | Pre-flighted by `xsearch.go`. Use `--last 7d` or shorter. Older needs paid tier. |
| SerpAPI | 100 searches/month free | HTTP 401 that reads like auth failure | Check the dashboard usage page — "tier exhausted" rendered as auth error. |
| Google CSE | 100 q/day free, then $5/1k | quota error mid-script, silent transition | Watch the Cloud Console quota; budget separately. |
| Google Gemini ask | 1,500 req/day on `gemini-2.5-flash` (free tier). The `gemini-flash-latest` and `gemini-2.5-pro` aliases require a paid Cloud project. | HTTP 429 with no clear "free-tier limit" indicator | Wait until UTC midnight or upgrade billing tier. |
| YouTube Data v3 | 10k units/day; `search` costs **100** units (not 1) | quota exhaustion much faster than expected | Use `videos`/`comments` calls (1 unit each) when possible; `search` only when needed. |
| GitHub | 60 req/hr unauthenticated | HTTP 403 rate-limit | Set `GITHUB_TOKEN` → 5,000 req/hr. |
| Reddit `search.json` | per-IP rate limit (undocumented exact threshold) | bursts return empty results | Space out queries; reduce parallelism (`-j 1`). |

---

## Silent / non-obvious behavioural quirks

**Tavily — `general` topic doesn't carry `published_date`.**
The default Tavily index returns most results without dates, so
`--last 7d` / `--after` silently include undated hits (we can't filter
what we can't date). For a guaranteed window, set
`TAVILY_TOPIC=news` in your env or `.env` — that switches Tavily to
the news-only index, which has dates upstream + much narrower recall
(often unhelpful for personal-name / evergreen queries).

**SerpAPI `google_ai_overview` — not every query qualifies.**
Google decides per-query whether to generate an AI Overview. When
it doesn't, the engine returns no `ai_overview` block and we surface
"no AI Overview returned for ..." — that's not a bug, it's a Google
ranking decision. Try a different phrasing or fall back to regular
search.

**DuckDuckGo result dates are unreliable.**
`--last` is best-effort on DDG. For strict date windows use Brave,
YouTube, Bluesky, X, HN, or arXiv — those have native date filters.

**Bing Search v7 is removed from social-fetch.**
Microsoft has been migrating it out of Cognitive Services since 2025
and we removed the `bing` provider in 0.2.0. A future Azure-backed
ask provider will replace it. If you need a paid web search today,
use Brave, SerpAPI, or Tavily.

---

## Auth landmines

**X — the bearer token in the dashboard is NOT what we want.**
We exchange `X_API_KEY` + `X_API_SECRET` for a bearer token
programmatically on every run. Setting `X_BEARER_TOKEN` does nothing.

**Bluesky — use an app password, never your account password.**
Generate at [bsky.app/settings/app-passwords](https://bsky.app/settings/app-passwords).
Format is `xxxx-xxxx-xxxx-xxxx`. App passwords are scoped + revocable
without nuking your full account.

**Perplexity — API key alone isn't enough.**
Even after generating `PERPLEXITY_API_KEY`, requests fail until you
attach a payment method to your Perplexity account. The error message
isn't always clear about this.

**OpenAI — no free tier.**
`OPENAI_API_KEY` requires billing enabled before any request works.
Tool calls (`web_search`) bill a per-call fee on top of token usage.

**Google APIs — three independent providers, three separate enables.**
Each API needs to be enabled separately in the Cloud Console:
Custom Search API (for `google` search) + a separate engine ID
(`GOOGLE_CSE_ID`), Generative Language API (for `gemini` ask;
`GEMINI_API_KEY` preferred, `GOOGLE_API_KEY` accepted as fallback),
YouTube Data API v3 (for `youtube` fetch + search;
`YOUTUBE_API_KEY`). The keys are independent — see API_KEYS.md for
the per-API setup. Note: Google removed the "Search the entire web"
toggle for new Custom Search Engines in 2024 — new CSEs are
restricted to listed sites only; prefer `serpapi` / `brave` /
`tavily` for general web search.

**Per-platform fetch chains live in the code, not in this doc.**
Each `internal/platforms/<name>/fetch.go` declares its own
`defaultChain` var; override per-call via `SOCIAL_FETCH_CHAIN_<NAME>`.
Run `social-fetch hints <name>` for the per-platform recipe (when
to override, what trade-offs apply, transport-specific quirks).
Don't rely on a static table here — it'd drift the moment a chain
default changes.

**LinkedIn search — use sparingly.**
LinkedIn aggressively rate-limits and occasionally temp-bans
accounts that scrape. The `linkedin` search provider works (drives
the browser to `/search/results/content/?keywords=...`, scroll-to-
bottom + wheel events to trigger lazy-load, parse the
`data-testid="expandable-text-box"` cards), but each query is a
real scrape against your account. Prefer `tavily` / `perplexity` /
`serpapi` for general "who's writing about X" questions, and only
reach for `-p linkedin` when LinkedIn-specific posts are
explicitly what you need. Running it back-to-back in a research
loop is exactly what gets accounts flagged.

**LinkedIn — keep the active tab on linkedin.com during a fetch.**
The bridge tells the extension to navigate the *active tab* to the
target URL. If you have Chrome focused on `chrome://extensions/`
or another non-LinkedIn page when a fetch fires, the navigate may
return before the page is actually rendered + observed and the
scrape sees a half-loaded page. Fix: leave a LinkedIn tab focused
in Chrome while running social-fetch.

---

## Cost surprises that look like errors

| provider | hidden cost | watch for |
|---|---|---|
| OpenAI `web_search` tool | per-invocation fee + token usage | long agent loops compound fast |
| Grok `web_search` tool | per-tool-invocation fee + token usage | same |
| Google CSE | $5 per 1k after first 100/day | silent transition from free to paid at request 101 |
| Anthropic `web_search` tool | $10 per 1k searches + token usage | larger questions trigger 3-5 searches per call |

---

## YouTube transcript chain fragility

| backend | state in 2026 |
|---|---|
| `ytdlp` | most reliable. Install: `brew install yt-dlp` or `pip install yt-dlp`. |
| `innertube` | fragile — YouTube renames internal fields every few months. Breaks silently. |
| `kkdai` | gated with HTTP 400s by YouTube. Often returns "no captions" for videos that have them. |

Default routing is `auto`: yt-dlp → innertube → kkdai (first success wins).
Set `YOUTUBE_TRANSCRIPT_PROVIDER=ytdlp` if you want to skip the others
and fail fast when yt-dlp isn't installed.

---

## Ledger daemon — sole owner of the SQLite ledger

`social-ledger daemon start` daemonises the ledger behind an HTTP
API on port 5557. Two operating modes:

- **Direct** (no daemon running): every caller — CLI, MCP,
  social-fetch's auto-ingest — opens the SQLite file directly.
  Today's behaviour, untouched.
- **Daemon** (daemon running): the daemon is the sole writer.
  All callers route through HTTP. Required for sandboxed agents
  (no filesystem access to the SQLite file) and remote MCP servers
  (different machine than the ledger).

```bash
social-ledger daemon start [--port 5557] [--bind 0.0.0.0:5557]
social-ledger daemon status                # one-shot snapshot
social-ledger daemon stop
```

When daemon is up, MCP's `social_fetch_fetch` returns
`content_url: http://daemon/content?url=…` instead of
`content_file: /tmp/…` — agents fetch the body over HTTP without
needing local file access.

Knobs:

| var | default | purpose |
|---|---|---|
| `SOCIAL_LEDGER_DAEMON_URL` | http://127.0.0.1:5557 | clients look here; set to remote URL for cross-host use |
| `SOCIAL_LEDGER_DAEMON_DISABLE` | unset | non-empty = clients always use direct store / subprocess |
| `SOCIAL_LEDGER_PROJECT` | `social_fetch` | per-project subdir under the data dir; `<base>/projects/<NAME>/`. Default `social_fetch` is the bucket every fetch lands in unless you set the var to switch contexts. Run separate daemons on different ports for separate projects. Pre-projects bare ledgers migrate automatically on first run. |

Don't run `social-ledger article add` (or any write subcommand)
while the daemon is up — both processes will fight for the SQLite
write lock. Stop the daemon first, run the CLI, restart the daemon.

---

## Local browser bookmarks (`social-fetch bookmarks`)

Reads Chrome's Bookmarks JSON file (per profile) and lists matching
entries — date-range filtered, folder-filtered, multi-profile aware.
Today's `--platform` value is `chrome` (default + only); future
platforms (Twitter bookmarks, Reddit saved posts) plug in as more
values.

```bash
social-fetch bookmarks list --since 2026-04-01            # added in April
social-fetch bookmarks list --folder-contains AI -n 20    # narrowed
social-fetch bookmarks list --all-profiles -f json        # every profile
social-fetch bookmarks profiles                           # available profiles
```

Reads the local Bookmarks JSON Chrome flushes to disk — no
extension or daemon needed. Bookmarks added moments before the
read may not appear (Chrome flushes within a second or two).

---

## Influencer directory (`social-ledger influencer`)

Track people / companies you treat as topic authorities — name,
socials, topics they're known for, free-form description, plus
which channels you actively want refreshed. Stored in the same
ledger as articles (source=`influencer`), so FTS picks them up
alongside fetched content.

```bash
social-ledger influencer add "Andrej Karpathy" --x karpathy --github karpathy --topics ai,research
social-ledger influencer subscribe "Andrej Karpathy" --platform x --topics ai
social-ledger influencer list --topic ai --followed
social-ledger influencer show andrej-karpathy --format json
```

Re-running `add` upserts: socials merge (new platform overwrites
same key, others kept), topics union, description replaces when
non-empty. Single-line update for "I just learned Jane's mastodon":
`add jane --social mastodon=@jane@hachyderm.io`.

The MCP layer mirrors the CLI as
`social_ledger_influencers_{list,get,add,remove,subscribe,unsubscribe}` —
agents can self-curate the watchlist mid-research.

Lookups are exact-slug-match. `Slugify("Andrej Karpathy")` →
`"andrej-karpathy"`; if the agent passes `"karpathy"` instead, that
slugifies to `"karpathy"` (different row, returns not-found). Pass
the full display name OR the canonical slug, not a partial.

---

## Headless browser pool — the local Chromium daemon

`social-fetch headless start` daemonises a pool of warm headless
Chromium browsers. Article / LinkedIn / Medium / Substack chains
include `headless` (chromedp under the hood) and route through the
daemon transparently when it's running — fetches drop from
~5-6s cold-spawn to ~3s warm-tab.

```bash
social-fetch headless start [--pool 2] [--recycle 50] [--port 5556]
social-fetch headless status                  # one-shot pool snapshot
social-fetch headless monitor                 # live-tailing TUI view
social-fetch headless stop
```

Knobs (env, with `--flag` equivalents on `start`):

| var | default | purpose |
|---|---|---|
| `SOCIAL_FETCH_HEADLESS_POOL_SIZE` | 2 | warm browsers (more = parallel batches) |
| `SOCIAL_FETCH_HEADLESS_RECYCLE_AFTER` | 50 | kill+respawn each browser after N fetches (anti-bot identity rotation; 0 disables) |
| `SOCIAL_FETCH_HEADLESS_DAEMON_URL` | http://127.0.0.1:5556 | clients look here; set to point at a remote daemon |
| `SOCIAL_FETCH_HEADLESS_DAEMON_DISABLE` | unset | non-empty = clients always use in-process spawn |
| `SOCIAL_FETCH_HEADLESS_USER_AGENT` | real-Chrome | UA the spawned browsers advertise |
| `SOCIAL_FETCH_HEADLESS_TIMEOUT` | 60s | per-fetch deadline including launch |
| `SOCIAL_FETCH_HEADLESS_SETTLE` | 2s | post-navigate sleep for JS hydration. The article fetcher auto-retries any thin (<100 char) response with a 6s settle once before falling through to the next chain method, so single-SPA flakes self-correct without operator action. Bump this for batch runs against many slow-hydrating sites. |

When daemon's down, the headless transport falls back to in-process
spawn so fetches still work — they just pay ~2s cold-start each.

Cookies are NOT honoured in daemon mode (anonymous-only). For
authenticated content (LinkedIn comments, Medium / Substack
member-only posts) use the bridge transport.

---

## CLI output gotchas

**Multiple URLs + `-f json` → auto-promoted to `jsonl`.**
A single JSON object can't represent a stream of items, so the CLI
emits one JSON line per result instead. Intentional, but surprising
the first time.

**`-j > 1` keeps input order anyway.**
Results are buffered per-slot and written as each slot completes in
sequence. Don't reach for `-j 1` just to preserve order — concurrency
+ ordered output is the default.

**`--log -` is your friend.**
Print every fetch / redirect / HTTP status to stderr. Faster than
diffing the output. Works on every subcommand.

---

## When in doubt

1. Run with `--log -` to see the actual HTTP requests + statuses.
2. Check the global audit log: `tail -f ~/Library/Caches/social-fetch/audit.jsonl`
   (or `social-fetch monitor`).
3. Confirm your `.env` is being loaded — add a non-existent key, run
   any subcommand, look for `warning: reading .env:` to know the file
   was found. (No warning + missing-key error = file not located.)
4. For DOM-scraped sources (LinkedIn, Reddit, X-syndication), if
   fields come back empty after a previously-working fetch, the third
   party probably renamed CSS classes. See `CLAUDE.md` "Selectors
   that scrape third-party DOMs will drift" for the fix pattern.
