---
name: socialfetch
description: Fetch content from social-media URLs (HackerNews, Reddit, GitHub, X/Twitter, LinkedIn, YouTube, Bluesky, arXiv, Medium, Substack, RSS, generic articles) and run web/social searches (DuckDuckGo, Brave, SerpAPI, Tavily, X, HN, YouTube, Bluesky, arXiv) — output as clean markdown or structured JSON. Use whenever the user asks to "pull", "fetch", "download", "summarise", or "search the web/Twitter/HN/YouTube/Bluesky/arxiv" for content at a URL or query.
allowed-tools: |
  Bash(scripts/socialfetch fetch *)
  Bash(scripts/socialfetch search *)
  Bash(scripts/socialfetch timeline *)
  Bash(scripts/socialfetch ask *)
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

## Subcommands

```
scripts/socialfetch fetch    <url> [<url>...]    [flags]
scripts/socialfetch search   "<query>"           [flags]
scripts/socialfetch timeline <user-or-url>       [flags]   recent activity for a user (X / LinkedIn)
scripts/socialfetch ask      "<question>"        [flags]   grounded answer engine (perplexity / grok / openai / anthropic / google / tavily / serpapi)
scripts/socialfetch research "<question>"        [flags]   EXPERIMENTAL — multi-angle research (decompose → parallel fan-out → synthesize)
scripts/socialfetch bridge   {start|stop|status|run}
```

Run `scripts/socialfetch --help` for the full reference. Output defaults to **markdown**; pass `-f json` or `-f jsonl` for structured input to other tools.

## Credentials (.env support)

Provider keys (`X_API_KEY`, `X_API_SECRET`, `TAVILY_API_KEY`, `SERPAPI_KEY`, `BRAVE_API_KEY`, `PERPLEXITY_API_KEY`, `XAI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`/`GOOGLE_API_KEY`/`GOOGLE_CSE_ID`, `YOUTUBE_API_KEY`, `BLUESKY_HANDLE`/`BLUESKY_APP_PASSWORD`, `GITHUB_TOKEN`) and routing hints (`HTML2MD_PROVIDER`, `HTML2MD_READER`, `YOUTUBE_TRANSCRIPT_PROVIDER`, `TAVILY_TOPIC`) can be set in the shell **or** placed in a `.env` file. At startup the binary loads, in order:

1. `./.env` (current working directory)
2. `<binary_dir>/.env` (sits next to the installed binary — typically `~/.claude/skills/socialfetch/.env`)

Already-exported shell vars always win over file entries.

## Decision rules

- **One URL → fetch it.** `scripts/socialfetch fetch <url>` auto-detects the source from the host (HN, Reddit, GitHub, X, RSS, or generic article).
- **A list of URLs → batch.** Pipe via stdin (`cat urls.txt | scripts/socialfetch fetch`) or use `-i FILE`. Add `-j 8` for parallel fetches; output stays in input order. **When to use batch:** the user already has ≥3 URLs in hand (pasted bookmarks, an RSS dump, a link list) and you don't need to reason between fetches. Connection-pool reuse + parallel workers make it ~3-4× faster than calling `fetch <url>` once per URL. **When NOT to use batch:** an iterative research loop where you'd fetch one URL, read it, then decide what to fetch next — call `fetch <url>` per URL so the result is cleanly attributed and you can reason between hops.
- **Save to disk →** `-o FILE` for one file, `-o DIR/` for one file per URL.
- **A user's recent posts → timeline.** `scripts/socialfetch timeline <user-or-url> [-p x|linkedin] [--kind ...] [-n N]`. Auto-detects the provider from URL; default for bare handles is X. See "Timeline subcommand" below.
- **A grounded question → ask.** `scripts/socialfetch ask "<question>" -p perplexity|grok|openai|anthropic|google|tavily|serpapi`. Returns synthesized answer + sources. Use this only when the user explicitly wants a synthesized answer; for raw documents use `fetch` or `search`.
- **A multi-angle research question → research (EXPERIMENTAL).** `scripts/socialfetch research "<question>" --max-angles 5 --jobs 4`. Decomposes into 3-8 angles, fans out parallel queries, synthesizes a final answer with citations. Use when you'd otherwise issue 4-8 manual queries. Costs roughly 2 LLM calls + N tool calls per question; use `ask` for simple lookups instead.
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

## Timeline subcommand

```
scripts/socialfetch timeline <user-or-url> [flags]
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
scripts/socialfetch timeline swyx --last 7d

# LinkedIn posts only (no reshares), markdown
scripts/socialfetch timeline patrickdebois -p linkedin --kind posts --no-reshares

# LinkedIn full deep-fetch (each item gets its body + comments)
scripts/socialfetch timeline matthewskelton -p linkedin --expand -n 10
```

## Ask subcommand

```
scripts/socialfetch ask "<question>" [flags]
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
scripts/socialfetch ask "what changed in the openai-microsoft revenue share clause" -p grok
scripts/socialfetch ask "best agent harness papers in the last month" -p perplexity --last month
scripts/socialfetch ask "what's the weather in NYC" -p auto                             # try the default chain
scripts/socialfetch ask "what's the weather in NYC" -p perplexity,anthropic,duckduckgo  # custom chain
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

**Setup once:** load `chrome-extension/` (at repo root) as an unpacked Chrome extension.

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
