---
name: socialfetch
description: Fetch content from social-media URLs (HackerNews, Reddit, GitHub, X/Twitter, LinkedIn, YouTube, Bluesky, arXiv, Medium, Substack, RSS, generic articles) and run web/social searches (DuckDuckGo, Bing, Brave, SerpAPI, Tavily, X, HN, YouTube, Bluesky, arXiv) — output as clean markdown or structured JSON. Use whenever the user asks to "pull", "fetch", "download", "summarise", or "search the web/Twitter/HN/YouTube/Bluesky/arxiv" for content at a URL or query.
allowed-tools: |
  Bash(scripts/socialfetch fetch *)
  Bash(scripts/socialfetch search *)
  Bash(scripts/socialfetch bridge start)
  Bash(scripts/socialfetch bridge stop)
  Bash(scripts/socialfetch bridge status)
  Bash(scripts/socialfetch bridge status *)
  Bash(scripts/socialfetch bridge run)
  Bash(scripts/socialfetch list)
  Bash(scripts/socialfetch help *)
  Bash(scripts/socialfetch version)
---

# socialfetch skill

Wraps the `socialfetch` Go binary at `scripts/socialfetch` (relative to this skill).

**Trust the CLI.** It is the authority for every fetch and search supported by this skill. Always shell out to `scripts/socialfetch` — never reimplement fetching with WebFetch, curl, custom parsers, or hand-rolled API calls, even if the binary returns empty results or an error you find surprising. If a fetch comes back empty, surface that to the user and (if appropriate) re-run with `--log -` to see audit lines, but do not try to "fix it" by going around the CLI.

## Three subcommands

```
scripts/socialfetch fetch  <url> [<url>...]   [flags]
scripts/socialfetch search "<query>"          [flags]
scripts/socialfetch bridge [--port N]
```

Run `scripts/socialfetch --help` for the full reference. Output defaults to **markdown**; pass `-f json` or `-f jsonl` for structured input to other tools.

## Credentials (.env support)

Provider keys (`X_API_KEY`, `X_API_SECRET`, `TAVILY_API_KEY`, `BING_API_KEY`, `SERPAPI_KEY`) can be set in the shell **or** placed in a `.env` file. At startup the binary loads, in order:

1. `./.env` (current working directory)
2. `<binary_dir>/.env` (sits next to the installed binary — typically `~/.claude/skills/socialfetch/.env`)

Already-exported shell vars always win over file entries.

## Decision rules

- **One URL → fetch it.** `scripts/socialfetch fetch <url>` auto-detects the source from the host (HN, Reddit, GitHub, X, RSS, or generic article).
- **A list of URLs → batch.** Pipe via stdin (`cat urls.txt | scripts/socialfetch fetch`) or use `-i FILE`. Add `-j 8` for parallel fetches; output stays in input order.
- **Save to disk →** `-o FILE` for one file, `-o DIR/` for one file per URL.
- **A query → search.** Pick the provider that matches the user's intent:
  - "search the web" / unspecified → `duckduckgo` (no auth)
  - "search Brave" / privacy-focused web → `brave` (needs `BRAVE_API_KEY`; native `--last 7d` via freshness)
  - "high-quality web search for AI agents" → `tavily` (needs `TAVILY_API_KEY`)
  - "search Bluesky" → `bluesky` (no auth, native date filter)
  - "search arXiv" / academic papers → `arxiv` (no auth, sorted newest-first)
  - "search HN" → `hackernews`
  - "search Twitter/X" → `x` (needs `X_API_KEY` + `X_API_SECRET`)
  - "search via Google" → `serpapi` (needs `SERPAPI_KEY`)
  - "search Bing" → `bing` (needs `BING_API_KEY`)
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
scripts/socialfetch fetch https://news.ycombinator.com/item?id=43000000

# Pull a Medium article → structured JSON
scripts/socialfetch fetch https://medium.com/@alice/some-post -f json

# Batch from a file → one .md file per URL in ./out/
scripts/socialfetch fetch -i bookmarks.txt -o out/ -j 8

# Pipe a list → JSONL stream
cat urls.txt | scripts/socialfetch fetch -f jsonl > all.jsonl

# Search the web, last 7 days, restrict to two domains
scripts/socialfetch search "vercel ai sdk" --last 7d --site vercel.com --site ai-sdk.dev

# HN search — top stories about a topic
scripts/socialfetch search "rust async" -p hackernews -n 20
```

## Listing supported sources/providers

```bash
scripts/socialfetch list
```

## Browser bridge (LinkedIn / Medium / Substack)

Three sources route through the local browser-extension bridge so the user's logged-in session is reused — that bypasses paywalls and member-only content.

| source | bridge required? | fallback |
| -- | -- | -- |
| **LinkedIn** | yes (no anonymous read path) | none — errors out |
| **Medium** | optional (paywall-aware via bridge) | direct HTTP for public excerpts |
| **Substack** | optional (paywall-aware via bridge) | direct HTTP for public excerpts |

Each fetched item carries `Extra.via = "bridge"` or `"http"` so you can tell which path produced the content.

### LinkedIn requires the bridge

**Setup once:** load `extension/` (at repo root) as an unpacked Chrome extension.

**Bridge lifecycle:**
```
scripts/socialfetch bridge start          # daemonize, write PID file
scripts/socialfetch bridge status         # connected / not connected / not running
scripts/socialfetch bridge stop           # graceful SIGTERM
scripts/socialfetch bridge run            # foreground (good for `nohup` or terminals)
```

**Always check status before fetching authenticated URLs:**
```
$ scripts/socialfetch bridge status
connected           # → fetch will work
not connected       # → bridge up but extension hasn't attached (open the browser)
bridge not running on :5555   # → run `bridge start` first
```
Exit codes are `0` connected / `1` not connected / `2` bridge not running, so agents can branch on them.

**Then fetch:**
```
scripts/socialfetch fetch https://www.linkedin.com/posts/foo-activity-700…
```
The bridge tells the extension to navigate the URL in your real browser, scrapes the rendered DOM, and returns clean markdown.

URLs the LinkedIn fetcher claims: `linkedin.com/posts/…`, `linkedin.com/feed/update/urn:li:activity:…`, `linkedin.com/in/<user>`, `linkedin.com/pulse/…`.

Errors you may see:
- `bridge unreachable` → start it (`bridge start`).
- `no extension connected` → open your browser; the extension reconnects every ~6s.

## YouTube

`scripts/socialfetch fetch <youtube-url>` claims `youtube.com/watch?v=…`, `youtu.be/…`, `youtube.com/shorts/…`, `youtube.com/live/…`, `youtube.com/embed/…`, and `music.youtube.com/…`.

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

## When NOT to use this skill

- The user wants to **post** content (this skill only reads).
- The URL is behind a paywall/login — output will be the gated stub. Tell the user.
- The URL needs a logged-in browser session (LinkedIn, X home feed, etc.) — not supported.
