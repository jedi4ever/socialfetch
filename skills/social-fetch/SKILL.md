---
name: social-fetch
description: Fetch content from social-media URLs (HackerNews, Reddit, GitHub, X/Twitter, LinkedIn, YouTube, Bluesky, arXiv, Medium, Substack, RSS, generic articles) and run web/social searches (DuckDuckGo, Brave, SerpAPI, Tavily, X, HN, YouTube, Bluesky, arXiv) — output as clean markdown or structured JSON. Use whenever the user asks to "pull", "fetch", "download", "summarise", or "search the web/Twitter/HN/YouTube/Bluesky/arxiv" for content at a URL or query.
allowed-tools: |
  Bash(scripts/social-fetch fetch *)
  Bash(scripts/social-fetch search *)
  Bash(scripts/social-fetch timeline *)
  Bash(scripts/social-fetch ask *)
  Bash(scripts/social-fetch research *)
  Bash(scripts/social-fetch bridge start)
  Bash(scripts/social-fetch bridge stop)
  Bash(scripts/social-fetch bridge status)
  Bash(scripts/social-fetch bridge status *)
  Bash(scripts/social-fetch bridge run)
  Bash(scripts/social-fetch headless start)
  Bash(scripts/social-fetch headless start *)
  Bash(scripts/social-fetch headless stop)
  Bash(scripts/social-fetch headless status)
  Bash(scripts/social-fetch headless status *)
  Bash(scripts/social-fetch headless monitor)
  Bash(scripts/social-fetch headless monitor *)
  Bash(scripts/social-fetch headless run)
  Bash(scripts/social-fetch bookmarks list)
  Bash(scripts/social-fetch bookmarks list *)
  Bash(scripts/social-fetch bookmarks profiles)
  Bash(scripts/social-fetch bookmarks profiles *)
  Bash(scripts/social-ledger daemon start)
  Bash(scripts/social-ledger daemon start *)
  Bash(scripts/social-ledger daemon stop)
  Bash(scripts/social-ledger daemon status)
  Bash(scripts/social-ledger daemon status *)
  Bash(scripts/social-ledger daemon run)
  Bash(scripts/social-fetch monitor *)
  Bash(scripts/social-fetch list)
  Bash(scripts/social-fetch hints)
  Bash(scripts/social-fetch hints *)
  Bash(scripts/social-fetch help *)
  Bash(scripts/social-fetch version)
  Bash(scripts/social-ledger article list *)
  Bash(scripts/social-ledger article search *)
  Bash(scripts/social-ledger article get *)
  Bash(scripts/social-ledger article stats)
  Bash(scripts/social-ledger article forget *)
  Bash(scripts/social-ledger article filter *)
  Bash(scripts/social-ledger article seen *)
  Bash(scripts/social-ledger article record *)
  Bash(scripts/social-ledger influencer *)
---

# social-fetch skill

Wraps the `social-fetch` Go binary at `scripts/social-fetch` (relative to this skill).

**Trust the CLI.** It is the authority for every fetch and search supported by this skill. Always shell out to `scripts/social-fetch` — never reimplement fetching with WebFetch, curl, custom parsers, or hand-rolled API calls, even if the binary returns empty results or an error you find surprising. If a fetch comes back empty, surface that to the user and (if appropriate) re-run with `--log -` to see audit lines, but do not try to "fix it" by going around the CLI.

**Before invoking any provider, run `scripts/social-fetch list` to see which platforms are usable in this environment.** Each provider is tagged with one of three states:

- `[ok]` — fully configured, fire away
- `[!auth]` — required env var not set; the row's suffix names which one (e.g. `→ missing BRAVE_API_KEY`). **Do not use these providers** — pick a different one in the same category. Suggesting the user configure the missing key is fine *only if they explicitly ask how*; don't proactively nag them about every unset key.
- `[bridge]` — needs the local browser bridge (LinkedIn / Medium / Substack / linkedin search & timeline). Only use after `scripts/social-fetch bridge status` reports connected.

The `auto` provider chains (`-p auto`) already skip unconfigured providers, so they're safe — but when an explicit provider name is needed (e.g. the user asks "search Twitter"), check `list` first to confirm `[ok]` status before invoking. For the MCP shape see `social_fetch_list_providers` — same data, structured as `{name, status, missing}` per category.

**For platform-specific quirks, run `scripts/social-fetch hints`** (no argument — dumps every platform's hints in one shot) before a search/fetch you haven't done recently. Captures things like "X recent search caps at 7 days strictly", "LinkedIn temp-bans accounts that scrape too fast", "Reddit anonymous search has worse relevance than `tavily site:reddit.com`". Pass a specific platform name to scope the output (e.g. `hints x`).

## Subcommands

```
scripts/social-fetch fetch    <url> [<url>...]    [flags]
scripts/social-fetch search   "<query>"           [flags]
scripts/social-fetch timeline <user-or-url>       [flags]   recent activity for a user (X / LinkedIn)
scripts/social-fetch ask      "<question>"        [flags]   grounded answer engine (perplexity / grok / openai / anthropic / gemini / tavily / serpapi)
scripts/social-fetch research "<question>"        [flags]   EXPERIMENTAL — multi-angle research (decompose → parallel fan-out → synthesize)
scripts/social-fetch bridge   {start|stop|status|run}
scripts/social-fetch headless {start|stop|status|monitor|run}
scripts/social-fetch bookmarks {list|profiles}              local browser bookmarks (chrome today; --platform NAME)
scripts/social-fetch hints    [<platform>]                  per-platform quirks, rate limits, gotchas (x / linkedin / reddit / ...)
```

Run `scripts/social-fetch --help` for the full reference. Output defaults to **markdown**; pass `-f json` or `-f jsonl` for structured input to other tools.

## Credentials (.env support)

Provider keys (`X_API_KEY`, `X_API_SECRET`, `TAVILY_API_KEY`, `SERPAPI_KEY`, `BRAVE_API_KEY`, `PERPLEXITY_API_KEY`, `XAI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`/`GOOGLE_API_KEY`/`GOOGLE_CSE_ID`, `YOUTUBE_API_KEY`, `BLUESKY_HANDLE`/`BLUESKY_APP_PASSWORD`, `GITHUB_TOKEN`) and routing hints (`HTML2MD_PROVIDER`, `HTML2MD_READER`, `YOUTUBE_TRANSCRIPT_PROVIDER`, `TAVILY_TOPIC`) can be set in the shell **or** placed in a `.env` file. At startup the binary loads, in order:

1. `./.env` (current working directory)
2. `<binary_dir>/.env` (sits next to the installed binary — typically `~/.claude/skills/social-fetch/.env`)

Already-exported shell vars always win over file entries.

## Decision rules

- **One URL → fetch it.** `scripts/social-fetch fetch <url>` auto-detects the source from the host (HN, Reddit, GitHub, X, RSS, or generic article).
- **Images / diagrams in the post →** every fetched item carries a `Media` list (LinkedIn post photos, X/Twitter media, Medium/Substack figures, YouTube thumbnails, generic article images). The MCP `social_fetch_fetch` envelope surfaces this as a `media[]` array of `{url, type, alt}` entries. When the user asks "what's on the picture / diagram / screenshot in this post", the agent's vision-capable Read tool (Claude Code / Claude Desktop) can read each `media[].url` directly — no extra fetch call. Empty `alt` usually means the image is worth looking at; populated `alt` is the author's caption. Same data is in the `## Media` section of the markdown content.
- **A list of URLs → batch.** Pipe via stdin (`cat urls.txt | scripts/social-fetch fetch`) or use `-i FILE`. Add `-j 8` for parallel fetches; output stays in input order.
- **Save to disk →** `-o FILE` for one file, `-o DIR/` for one file per URL.
- **A user's recent posts → timeline.** `scripts/social-fetch timeline <user-or-url> [-p x|linkedin] [--kind ...] [-n N]`. Auto-detects the provider from URL; default for bare handles is X. See "Timeline subcommand" below.
- **A grounded question → ask.** `scripts/social-fetch ask "<question>" -p perplexity|grok|openai|anthropic|gemini|tavily|serpapi`. Returns synthesized answer + sources. Use this only when the user explicitly wants a synthesized answer; for raw documents use `fetch` or `search`.
- **A multi-angle research question → research (EXPERIMENTAL).** `scripts/social-fetch research "<question>" --max-angles 5 --jobs 4`. Decomposes into 3-8 angles, fans out parallel queries, synthesizes a final answer with citations. Use when you'd otherwise issue 4-8 manual queries. Costs roughly 2 LLM calls + N tool calls per question; use `ask` for simple lookups instead.
- **A query → search.** Pick the provider that matches the user's intent. `-p auto` walks `perplexity → tavily → brave → serpapi → duckduckgo`; comma-lists like `-p tavily,duckduckgo` define a custom fallback order. Each falls through on missing key / error / 0 results.
  - "search the web" / unspecified → `duckduckgo` (no auth)
  - "search Brave" / privacy-focused web → `brave` (needs `BRAVE_API_KEY`; native `--last 7d` via freshness)
  - "high-quality web search for AI agents" → `tavily` (needs `TAVILY_API_KEY`)
  - "Perplexity index without synthesis" → `perplexity` (needs `PERPLEXITY_API_KEY`; same key as `ask -p perplexity`, but cheaper since no LLM tokens)
  - "LinkedIn posts about <topic>" → `linkedin` (requires the browser bridge + a logged-in LinkedIn session; up to 50 results per query via scroll-to-bottom + wheel-event lazy-load). **Use sparingly.** LinkedIn aggressively rate-limits accounts that scrape — running this back-to-back will get the user temp-banned. Prefer `tavily` / `perplexity` / `serpapi` for general "who's writing about X" questions, and only reach for `-p linkedin` when LinkedIn-specific posts are explicitly the goal.
  - "search Bluesky" → `bluesky` (no auth, native date filter)
  - "search arXiv" / academic papers → `arxiv` (no auth, sorted newest-first)
  - "search HN" → `hackernews`
  - "search Reddit" → `reddit` (no auth, public search.json; rate-limited per IP)
  - "search Twitter/X" → `x` (needs `X_API_KEY` + `X_API_SECRET`)
  - "search via Google" → `serpapi` (needs `SERPAPI_KEY`)
  - "search YouTube" → `youtube` (needs `YOUTUBE_API_KEY`; supports `--last 7d` / `--after` natively, dates are strict)

## Flags worth remembering

| flag | when |
| -- | -- |
| `-f markdown\|json\|jsonl` | format (default markdown) |
| `-o PATH` | stdout / FILE / DIR/ |
| `-i FILE` | URLs file (`-` = stdin; auto-detected when piped) |
| `-j N` | parallel workers for batch fetch |
| `--no-comments` | skip comment trees on HN/Reddit/X |
| `--max-comments N` | cap comments per item |
| `--generic-extraction` | force the catch-all article extractor (debug) |
| `--log -` | print per-fetch audit lines to stderr |

Search-only:
| flag | when |
| -- | -- |
| `-p PROVIDER` | pick search provider |
| `-n N` | max results |
| `--after YYYY-MM-DD` / `--before YYYY-MM-DD` / `--last 7d` | date filters |
| `--site DOMAIN` / `--exclude-site DOMAIN` | domain filters (repeatable) |

## Examples

```bash
# Pull a HN story with comments → markdown to stdout
scripts/social-fetch fetch https://news.ycombinator.com/item?id=43000000

# Pull a Medium article → structured JSON
scripts/social-fetch fetch https://medium.com/@alice/some-post -f json

# Batch from a file → one .md file per URL in ./out/
scripts/social-fetch fetch -i bookmarks.txt -o out/ -j 8

# Pipe a list → JSONL stream
cat urls.txt | scripts/social-fetch fetch -f jsonl > all.jsonl

# Search the web, last 7 days, restrict to two domains
scripts/social-fetch search "vercel ai sdk" --last 7d --site vercel.com --site ai-sdk.dev

# HN search — top stories about a topic
scripts/social-fetch search "rust async" -p hackernews -n 20
```

## Timeline subcommand

```
scripts/social-fetch timeline <user-or-url> [flags]
  -p PROVIDER         x (default for bare handles) | linkedin
  --kind KIND         x:        all (default), tweets, replies, retweets
                      linkedin: all (default), posts, comments, reactions
  -n N                max items (default 30)
  --last DUR          sugar for --after (e.g. 7d, 24h)
                      x has a hard 7-day cap
  --after / --before  yyyy-mm-dd or RFC3339
  --expand            (LinkedIn) re-fetch each item via the post fetcher (slow)
  --no-reshares       (LinkedIn) drop reposts from the timeline
```

User identifier accepts:
- `swyx` (bare handle → x)
- `@swyx` (`@` implies x)
- `https://x.com/swyx` (auto-detected)
- `https://www.linkedin.com/in/patrickdebois/` (auto-detected)
- `patrickdebois` + `-p linkedin`

LinkedIn timelines drive the bridge through scroll/get_html cycles. Returns ~5–50 items depending on how active the user is. **Bridge required for LinkedIn — check `bridge status` first.** X timelines wrap recent-search; the 7-day cap applies and the binary pre-flights it with a clear error.

```bash
# Last 7d on X
scripts/social-fetch timeline swyx --last 7d

# LinkedIn posts only (no reshares), markdown
scripts/social-fetch timeline patrickdebois -p linkedin --kind posts --no-reshares

# LinkedIn full deep-fetch (each item gets its body + comments)
scripts/social-fetch timeline matthewskelton -p linkedin --expand -n 10
```

## Ask subcommand

```
scripts/social-fetch ask "<question>" [flags]
  -p PROVIDER     perplexity (default), grok, openai, anthropic, google, tavily, serpapi
                  special values:
                    auto             try the built-in chain in order
                                     (perplexity → grok → openai → anthropic →
                                      google → tavily → serpapi)
                    name1,name2,…    comma-list to try in order
  -m MODEL        override the provider's default (empty = provider picks where supported)
  --last WINDOW   day | week | month | year (provider-dependent)
  --max-tokens N  cap response length
  --instructions  system-prompt-style preamble (alias: --system)
                  honored by perplexity / grok / openai / anthropic / google;
                  ignored by tavily / serpapi (no system-prompt support)
```

Returns a synthesized answer plus a numbered Sources list. Auth needed per provider — see Credentials above.

When `-p auto` or a comma-list is given, each provider in turn falls through on (a) missing API key, (b) upstream error, or (c) empty answer — the next provider gets a try, and the first non-empty response wins. The audit log records which provider answered.

```bash
scripts/social-fetch ask "what changed in the openai-microsoft revenue share clause" -p grok
scripts/social-fetch ask "best agent harness papers in the last month" -p perplexity --last month
scripts/social-fetch ask "what's the weather in NYC" -p auto                             # try the default chain
scripts/social-fetch ask "what's the weather in NYC" -p perplexity,anthropic,duckduckgo  # custom chain
```

## Listing supported sources/providers

```bash
scripts/social-fetch list
```

## Transports — bridge / headless / http / jina

The fetcher walks a **chain of transports** per platform; each fetched item carries `Extra.via` naming which one produced the body. Defaults live in the platform Go code; override per call via `SOCIAL_FETCH_CHAIN_<NAME>`. Run `scripts/social-fetch hints <platform>` for the per-platform recipe.

| transport | what it does | when needed |
| -- | -- | -- |
| **`bridge`** | drives your real, logged-in Chrome via the extension | auth-walled content (LinkedIn comments, Medium / Substack member-only posts) |
| **`headless`** | local stealth Chromium via chromedp; anonymous, JS-rendering | JS-rendered SPAs, soft anti-bot — also the article fetcher's preferred path |
| **`http`** | plain HTTP GET | static pages where JS isn't needed |
| **`jina`** | remote `r.jina.ai` service | last-resort catch-all when local methods fail |

Defaults (mirrored from code — `social-fetch hints <platform>` is canonical):

| platform | default chain |
| -- | -- |
| article | `headless,http,bridge,jina` |
| linkedin | `headless,bridge,jina` (bridge for comments) |
| medium / substack | `bridge,http,headless,jina` (bridge for paywall) |
| twitter | `api,syndication,jina` |

### LinkedIn requires the bridge

**Setup once:** load `extensions/chrome/` (at repo root) as an unpacked Chrome extension.

**Bridge lifecycle:**
```
scripts/social-fetch bridge start          # daemonize, write PID file
scripts/social-fetch bridge status         # connected / not connected / not running
scripts/social-fetch bridge stop           # graceful SIGTERM
scripts/social-fetch bridge run            # foreground (good for `nohup` or terminals)
```

**Always check status before fetching authenticated URLs:**
```
$ scripts/social-fetch bridge status
connected           # → fetch will work
not connected       # → bridge up but extension hasn't attached (open the browser)
bridge not running on :5555   # → run `bridge start` first
```
Exit codes are `0` connected / `1` not connected / `2` bridge not running, so agents can branch on them.

**Then fetch:**
```
scripts/social-fetch fetch https://www.linkedin.com/posts/foo-activity-700…
```
The bridge tells the extension to navigate the URL in your real browser, scrapes the rendered DOM, and returns clean markdown.

URLs the LinkedIn fetcher claims: `linkedin.com/posts/…`, `linkedin.com/feed/update/urn:li:activity:…`, `linkedin.com/in/<user>`, `linkedin.com/pulse/…`.

Errors you may see:
- `bridge unreachable` → start it (`bridge start`).
- `no extension connected` → open your browser; the extension reconnects every ~6s.

### Local browser bookmarks (`scripts/social-fetch bookmarks`)

Reads Chrome's local Bookmarks JSON and lists matching entries. Date-range filterable, multi-profile aware.

```
scripts/social-fetch bookmarks list                                # newest 100, default profile
scripts/social-fetch bookmarks list --since 2026-04-01             # bookmarked since April
scripts/social-fetch bookmarks list --folder-contains AI -n 20     # fuzzy folder match
scripts/social-fetch bookmarks list --folder "Bookmarks bar/AI"    # exact subtree (AI/, AI/papers/, …)
scripts/social-fetch bookmarks list --all-profiles -f json         # every profile, JSON
scripts/social-fetch bookmarks profiles                            # which profiles exist
```

`--platform chrome` is the default. Future platforms (Twitter bookmarks, Reddit saved posts — server-side, account-scoped) will plug in as additional values.

**Scope every call to one folder via env var**: set
`SOCIAL_FETCH_BOOKMARKS_ROOT_FOLDER="Bookmarks bar/AI"` once and the
agent's `bookmarks list` calls (CLI + MCP) only see bookmarks under
that folder + every nested subfolder. Override per-call with `--folder`.

### Ledger daemon (sandboxed / remote MCP)

`social-ledger daemon start` daemonises the SQLite ledger behind an HTTP API on port 5557. When it's running, every caller — CLI, social-fetch's auto-ingest, MCP read tools — routes through HTTP instead of opening the SQLite file directly.

```
scripts/social-ledger daemon start
scripts/social-ledger daemon status
scripts/social-ledger daemon stop
```

In daemon mode, `social_fetch_fetch` returns `content_url` (HTTP pointer) instead of `content_file` (local path). Agents that don't have filesystem access to the daemon's host can still read fetched bodies. For local single-machine usage, leave it off — direct file access is faster (~10ms saved per call).

**Multi-project ledgers**: every ledger lives under `<base>/projects/<NAME>/`. The default project is `social_fetch`; set `SOCIAL_LEDGER_PROJECT=research-x` to switch to a separate ledger for that context. Pre-projects bare ledgers migrate automatically on first run (no operator action needed). One daemon serves one project; run multiple daemons on different ports for parallel projects.

### Headless browser pool (faster anonymous fetches)

Separate from the bridge. `headless start` daemonises a pool of warm headless Chromium browsers. Article / LinkedIn / Medium / Substack chains include `headless`; with the daemon running fetches drop from ~5–6s (cold spawn) to ~3s (warm tab). Anonymous-only — no session reuse, never touches the user's real Chrome profile.

```
scripts/social-fetch headless start                  # default pool=2 recycle=50
scripts/social-fetch headless status                 # one-shot pool snapshot
scripts/social-fetch headless monitor                # live-tailing TUI view
scripts/social-fetch headless stop
```

When the daemon's down, headless transparently falls back to per-call spawn — fetches still work, just slower. Run `scripts/social-fetch headless --help` for tuning flags (`--pool N`, `--recycle N`, `--port N`).

**When to start it:** before any batch fetch with `-j > 1`, before research loops, when fetching JS-rendered articles where `http` returns a thin shell.

## YouTube

`scripts/social-fetch fetch <youtube-url>` claims `youtube.com/watch?v=…`, `youtu.be/…`, `youtube.com/shorts/…`, `youtube.com/live/…`, `youtube.com/embed/…`, and `music.youtube.com/…`.

- **Metadata**: pure scraping via `kkdai/youtube/v2` — no auth needed.
- **Transcript**: configurable provider chain (see below). Appended to `Content` under a `## Transcript` heading; structured timed segments live in `Extra.transcript`.
- **Comments**: optional, gated on `YOUTUBE_API_KEY` (free Google Cloud key, 10,000 units/day). Without it, comments are skipped silently.

### Transcript provider switching

Set `YOUTUBE_TRANSCRIPT_PROVIDER` to control which transcript backend is used:

| value | behavior |
| -- | -- |
| `auto` (default) | yt-dlp if installed → InnerTube (no auth) → kkdai. First success wins. |
| `ytdlp` | shells out to `yt-dlp` (most reliable; install with `brew install yt-dlp` or `pip install yt-dlp`) |
| `innertube` | pure-Go scrape via `youtubei/v1/get_transcript` — fragile (YouTube can break it) but no extra runtime dep |
| `kkdai` | the kkdai library's caption-track endpoint; YouTube has been gating this with HTTP 400s in 2026 |

Set `YOUTUBE_API_KEY` in your shell or `.env` for comments. Some videos have transcripts disabled by the channel — the fetcher logs that case and returns metadata + comments only.

## Tavily date filter caveat

Tavily's `general` topic (the default — high relevance) doesn't populate `published_date` for most results, so `--last 7d` / `--after` enforce date strictly only on results we *can* date. Set `TAVILY_TOPIC=news` (in env or `.env`) when you want a guaranteed window — that switches Tavily's index to news-only, which has dates upstream + much narrower recall (often unhelpful for personal-name or evergreen-topic queries).

## X / Twitter reply behavior

When `X_API_KEY` + `X_API_SECRET` are set, fetching a tweet also pulls its replies as a nested tree (one batched `tweets/search/recent` call per 100 replies — no per-reply round-trips). Caveats:

- Search is limited to the **last 7 days** by X's API tier — older tweets return 0 replies. The audit log (`--log -`) makes this explicit.
- Without creds, the syndication fallback is used and returns 0 replies (no API support).
- `--no-comments` and `--max-comments N` apply.

## Ledger (history of every fetch)

A companion binary `scripts/social-ledger` ships alongside
`scripts/social-fetch`. When both are present (always true for this
skill), every successful `fetch` / `timeline` / `research` call is
auto-recorded — content into a SQLite + FTS5 store, plus a
markdown mirror tree on disk. No env-var setup needed; auto-detect
flips it on.

Useful queries against the ledger from inside the skill:

```bash
scripts/social-ledger article list                         # newest first
scripts/social-ledger article list --source hackernews     # filter by source
scripts/social-ledger article search "harness engineering" # full-text search
scripts/social-ledger article get https://example.com/foo  # one item back
scripts/social-ledger article stats                        # counts + sizes
scripts/social-ledger article forget https://...           # drop one entry
```

When the user asks a question that resembles "have we seen X
before?" or "what did we learn about Y last week?" — the ledger is
where to look first, before re-fetching.

To explicitly disable: set `SOCIAL_LEDGER=0` in the env. To
override the storage location: `SOCIAL_LEDGER_DIR=...`
(default `~/.local/share/social-ledger`).

## When NOT to use this skill

- The user wants to **post** content (this skill only reads).
- The URL is behind a paywall/login — output will be the gated stub. Tell the user.
- The URL needs a logged-in browser session (LinkedIn, X home feed, etc.) — not supported.
