# socialfetch — hints & gotchas

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
| Google Gemini ask | 1,500 req/day on `gemini-flash-latest` | HTTP 429 with no clear "free-tier limit" indicator | Wait until UTC midnight or upgrade billing tier. |
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

**Bing Search v7 is being retired.**
Microsoft has been migrating it out of Cognitive Services since 2025.
The resource is still creatable but availability varies by Azure
region. If you're starting fresh, prefer Brave or Tavily.

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

**Google APIs — each API must be enabled separately.**
Even with one `GOOGLE_API_KEY`, you have to go to the Cloud Console
and enable **each** of: Custom Search API (for `google` search),
Generative Language API (for `google` ask), YouTube Data API v3 (for
`youtube` fetch + search). One shared key, three separate "Enable"
buttons.

**LinkedIn — no anonymous read path.**
Every LinkedIn fetch / timeline goes through the bridge. Always run
`socialfetch bridge status` before fetching authenticated URLs.
Exit codes: `0` connected / `1` not connected / `2` bridge not running.

---

## Cost surprises that look like errors

| provider | hidden cost | watch for |
|---|---|---|
| OpenAI `web_search` tool | per-invocation fee + token usage | long agent loops compound fast |
| Grok `web_search` tool | per-tool-invocation fee + token usage | same |
| Google CSE | $5 per 1k after first 100/day | silent transition from free to paid at request 101 |
| Bing v7 | Azure metered (per region) | quota tied to Azure billing, not a flat cap |

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
2. Check the global audit log: `tail -f ~/Library/Caches/socialfetch/audit.jsonl`
   (or `socialfetch monitor`).
3. Confirm your `.env` is being loaded — add a non-existent key, run
   any subcommand, look for `warning: reading .env:` to know the file
   was found. (No warning + missing-key error = file not located.)
4. For DOM-scraped sources (LinkedIn, Reddit, X-syndication), if
   fields come back empty after a previously-working fetch, the third
   party probably renamed CSS classes. See `CLAUDE.md` "Selectors
   that scrape third-party DOMs will drift" for the fix pattern.
