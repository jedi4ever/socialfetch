# socialfetch

**A toolkit that lets AI agents read and reason over the social web.**

LLMs are great at understanding text but bad at getting it. The
social web — HackerNews threads, Reddit comments, GitHub repos,
X/Twitter posts, LinkedIn timelines, YouTube transcripts, Bluesky
feeds, arXiv papers, Medium / Substack articles, RSS feeds, generic
blog posts — lives behind a different API, DOM scraper, auth flow,
and rate limit per platform. socialfetch hides all of that behind one
consistent interface and gives the agent **clean Markdown or
structured JSON** an LLM can actually parse.

Same shape covers grounded web search (Perplexity, Tavily, Brave,
SerpAPI, Google, DuckDuckGo) and grounded answer engines (Grok,
OpenAI, Anthropic, Gemini), and exposes everything as MCP tools so
**Claude Desktop, Claude Code, claude.ai, and Perplexity** can call
into it as a first-class tool — not as another `WebFetch` round-trip
that returns rendered HTML.

```bash
socialfetch fetch    https://news.ycombinator.com/item?id=43000000
socialfetch search   "vercel ai sdk" -p auto -n 10 --last 7d
socialfetch ask      "what changed in Go 1.27?" -p perplexity
socialfetch timeline @jedi4ever -p x --last 24h
socialfetch research "tessl harness engineering" -p anthropic
```

## What it is

- **One interface for ~20 platforms.** Same `Item` shape whether
  you're scraping HN comments, pulling a LinkedIn timeline, or
  asking Perplexity a recency-filtered question — agents don't need
  per-platform branching logic.
- **Provider chains.** `-p auto` walks a default fallback list, so
  whichever subset of API keys you've configured determines coverage
  and the rest silently no-op. Drop a key in, the agent gets a new
  capability without code changes.
- **MCP server built in.** `socialfetch mcp` exposes every CLI
  capability as Model Context Protocol tools, runnable over stdio
  (Claude Desktop) or Streamable HTTP (claude.ai, Perplexity, Claude
  Code remote-MCP). Same binary is your CLI and your MCP server.
- **Browser bridge** for authenticated paths — LinkedIn, Medium /
  Substack paywalls — via a small Chrome MV3 extension that brokers
  between the agent and your real, logged-in browser. Public content
  still goes direct over HTTP.
- **Citations first.** Every result carries `source`, `url`,
  `fetched_at`, `written_at`, scores, comment trees — so the agent
  can ground its answer in something the user can click back to.

## Install

Four distribution channels, all wrapping the same Go binary. Pick
whichever matches your install style — full step-by-step in
[INSTALL.md](INSTALL.md).

| Channel | When to use | Install |
|---|---|---|
| **Claude Desktop Extension (`.mcpb`)** | One-click install with API-key prompts in macOS Keychain | Download from [Releases](https://github.com/jedi4ever/socialfetch/releases/latest) → drag the `.mcpb` into Claude Desktop → Settings → Extensions |
| **Remote MCP (ngrok)** | claude.ai, Perplexity, Claude Code remote MCP | `socialfetch mcp --ngrok` — prints a public URL + bearer token to paste into your client |
| **Skill** | Claude Desktop or Claude Code, file-based with manual `.env` | `make skill-install` — copies `SKILL.md` + binary to `~/.claude/skills/socialfetch/` |
| **Claude Code plugin (marketplace)** | One-line install for Claude Code users | `/plugin marketplace add jedi4ever/socialfetch` then `/plugin install socialfetch` (requires `socialfetch` on PATH separately) |

Bare CLI for shell scripts:

```bash
go install github.com/jedi4ever/socialfetch/cmd/socialfetch@latest
# or download a platform binary from the releases page:
#   socialfetch-0.9.0-darwin-arm64.tar.gz
#   socialfetch-0.9.0-darwin-amd64.tar.gz
#   socialfetch-0.9.0-linux-amd64.tar.gz
```

Build from source:

```bash
git clone https://github.com/jedi4ever/socialfetch.git
cd socialfetch && make build       # → ./dist/socialfetch
```

Requires Go 1.26+. Windows is not currently supported (the bridge
daemon uses Unix-only syscalls — run via WSL).

## Platforms supported

### Fetch (URL → structured Item)

| Source | Example URL | Auth |
|---|---|---|
| `hackernews` | `news.ycombinator.com/item?id=…` or bare ID | none (Firebase API) |
| `reddit` | `reddit.com/r/<sub>/comments/<id>/…` | none (`.json` endpoint) |
| `github` | `github.com/<owner>/<repo>` | optional `GITHUB_TOKEN` (60→5000 req/hr) |
| `twitter` | `x.com/<user>/status/<id>` | optional `X_API_KEY`+`X_API_SECRET` (long-form notes + replies) |
| `linkedin` | `linkedin.com/posts/…`, `/feed/update/…`, `/in/<user>`, `/pulse/…` | **bridge required** |
| `youtube` | `youtube.com/watch?v=…`, `/shorts/…`, `/live/…`, `/embed/…`, `youtu.be/…` | optional `YOUTUBE_API_KEY` for comments |
| `bluesky` | `bsky.app/profile/<handle>/post/<rkey>` | none (public AppView) |
| `arxiv` | `arxiv.org/abs/<id>`, `/pdf/<id>`, `/html/<id>` | none |
| `medium` | `medium.com/…`, `*.medium.com` | bridge-first for paywall, HTTP fallback |
| `substack` | `*.substack.com` | bridge-first for member-only, HTTP fallback |
| `rss` | URLs whose path contains `/feed`, `/rss`, `/atom`, or ends in `.xml` | none (RSS 2.0 + Atom) |
| `article` | any other `http(s)` URL | catch-all: OpenGraph + JSON-LD + article body |

### Search

| Provider | Auth | Notes |
|---|---|---|
| `duckduckgo` | none | Default for unauthed; date filter is best-effort |
| `google` | `GOOGLE_API_KEY` + `GOOGLE_CSE_ID` | 100 q/day free, then $5/1k |
| `brave` | `BRAVE_API_KEY` | 2,000 q/mo free; native `--last 7d` |
| `serpapi` | `SERPAPI_KEY` | 100 searches/mo free; Google-results proxy |
| `tavily` | `TAVILY_API_KEY` | AI-tuned, scored, domain include/exclude |
| `perplexity` | `PERPLEXITY_API_KEY` | Same index as Sonar — strong AI/news/research signal |
| `hackernews` | none | Algolia public search |
| `reddit` | none | Per-IP rate limit |
| `x` (Twitter) | `X_API_KEY` + `X_API_SECRET` | Recent search, **7-day window** on free tier |
| `youtube` | `YOUTUBE_API_KEY` | 100 units per `search.list` (~100 searches/day free) |
| `bluesky` | `BLUESKY_HANDLE` + `BLUESKY_APP_PASSWORD` | Native date filters |
| `arxiv` | none | Atom search API; client-side date filter |
| `linkedin` | bridge + login | **Use sparingly** — aggressive rate limits |

### Ask (grounded answer engines)

| Provider | Auth | Notes |
|---|---|---|
| `perplexity` | `PERPLEXITY_API_KEY` | Sonar models — strongest grounded recall |
| `grok` | `XAI_API_KEY` | xAI's `/v1/responses` Agent Tools API |
| `openai` | `OPENAI_API_KEY` | GPT with `web_search` tool — billing must be enabled |
| `anthropic` | `ANTHROPIC_API_KEY` | Claude with `web_search` tool — $10 per 1k searches |
| `google` | `GOOGLE_API_KEY` | Gemini Generative Language API |
| `tavily` | `TAVILY_API_KEY` | Search-then-summarize |
| `serpapi` | `SERPAPI_KEY` | Google AI Overview pass-through (per-query availability) |

`-p auto` walks the default chain; `-p name1,name2` runs your own
fallback order. `-p auto` skips providers without keys configured.

### Timeline (recent activity for a user)

| Platform | Auth |
|---|---|
| `x` (Twitter) | `X_API_KEY` + `X_API_SECRET` (7-day window on free tier) |
| `linkedin` | bridge + login |

## CLI commands

```
socialfetch fetch     <url> [<url>…]      pull URL(s) into structured Item(s)
socialfetch search    "<query>"           run a search query
socialfetch ask       "<question>"        ask a grounded answer engine
socialfetch timeline  <user-or-url>       recent activity for a user
socialfetch research  "<question>"        EXPERIMENTAL multi-angle research
socialfetch mcp       [flags]             run as an MCP server (stdio or HTTP)
socialfetch bridge    {start|stop|status|run}    Chrome browser-bridge daemon
socialfetch monitor   [--tail N]          tail the global audit log
socialfetch list                          list every fetch / search / ask / timeline provider
socialfetch help      [subcommand]        full flag reference per subcommand
socialfetch version                       print the version
```

`socialfetch help <subcommand>` is the authoritative flag reference —
every flag has a short and long form; output is shaped to be parseable
by agents.

### `fetch`

```
socialfetch fetch <url>... [-f markdown|json|jsonl] [-o -|FILE|DIR/]
                           [-i FILE|-] [-j N] [--no-comments]
                           [--max-comments N] [--timeout DUR] [-l -|FILE]
```

Multiple URLs + `-f json` auto-promotes to `jsonl` (one item per
line). Stdin auto-detected when piped: `cat urls.txt | socialfetch fetch`.
`-j N` keeps results in input order despite concurrency.

### `search`

```
socialfetch search "<query>" [-p auto|<provider>|<chain>] [-n N]
                             [--last 7d|--after YYYY-MM-DD|--before …]
                             [--site DOMAIN[,…]] [-f markdown|json|jsonl]
```

Date filters are native where the provider supports them and
client-side otherwise. See HINTS.md for per-provider date-filter
quirks.

### `ask`

```
socialfetch ask "<question>" [-p auto|<provider>|<chain>] [--model NAME]
                             [--recency day|week|month|year]
                             [--max-tokens N] [--instructions "…"]
```

`--recency` is honored where the upstream supports it; `--instructions`
is the system-prompt-style preamble (perplexity, grok, openai,
anthropic, google).

### `timeline`

```
socialfetch timeline <handle-or-url> [-p x|linkedin]
                                     [--kind all|tweets|replies|retweets|posts|comments|reactions]
                                     [--last 7d] [-n N] [--expand] [--no-reshares]
```

Auto-detects the platform from the URL or `@-prefix`. `--expand`
deep-fetches each post (LinkedIn — slower but fuller content).

### `research` (experimental)

```
socialfetch research "<question>" [--orchestrator <ask-provider>]
                                  [--max-angles N] [-j N] [--json]
```

Decomposes the question into 3-8 angles, fans out parallel
fetch/search/ask/timeline calls, and synthesizes a markdown answer
with citations. Costs ~2 LLM calls + N tool calls per question. Use
when you'd otherwise issue 4-8 manual queries.

### `mcp`

```
socialfetch mcp                       # stdio (Claude Desktop Extension)
socialfetch mcp --http :PORT          # Streamable HTTP (claude.ai, ngrok)
socialfetch mcp --ngrok               # spawn ngrok automatically
```

Exposes `socialfetch_fetch`, `_search`, `_ask`, `_timeline`,
`_research`, `_list_providers`, `_bridge_status` as MCP tools. Set
`MCP_AUTH_TOKEN` for HTTP mode (auto-generated when `--ngrok` is
combined with no env var). HTTP-mode tee's tool calls + outbound
platform HTTP traffic to stderr live.

### `bridge`

```
socialfetch bridge start         # daemonize on :5555
socialfetch bridge status        # exit 0 connected / 1 not connected / 2 not running
socialfetch bridge stop          # SIGTERM
socialfetch bridge run           # foreground (no daemon)
```

One-time setup: load `chrome-extension/` in `chrome://extensions/`
(Developer mode → Load unpacked). Required for LinkedIn, Medium /
Substack paywall fetches.

### `monitor`

```
socialfetch monitor                  # follow ~/Library/Caches/socialfetch/audit.jsonl
socialfetch monitor --tail 200       # last N lines then follow
```

The audit log is always-on across CLI, MCP-stdio, and MCP-HTTP runs.
Useful for tailing what an agent is actually invoking from another
shell.

## Output format

Every output — JSON or markdown — includes both `fetched_at` (when
the data was pulled) and `written_at` (when this output was rendered)
plus author, source, score, tags, and comment trees where applicable.
JSON output uses a stable `Envelope { written_at, item }` shape; JSONL
emits one envelope per line.

## YouTube transcripts

Three transcript backends, switchable via `YOUTUBE_TRANSCRIPT_PROVIDER`:

| backend | how | trade-offs |
|---|---|---|
| `ytdlp` | shells out to [yt-dlp](https://github.com/yt-dlp/yt-dlp) | Most reliable. Install with `brew install yt-dlp` or `pip install yt-dlp`. |
| `innertube` | pure Go; scrapes the watch page → POSTs to `youtubei/v1/get_transcript` | No extra dep; uses YouTube's private API (breaks silently when YouTube changes the schema). |
| `kkdai` | `github.com/kkdai/youtube/v2`'s caption-track endpoint | Legacy timedtext URL; YouTube has been gating it with HTTP 400 throughout 2026. |

`auto` (default) tries them in order yt-dlp → innertube → kkdai.

## Credentials

All API keys are **optional** — features gated on a missing key
degrade gracefully. The binary auto-loads `.env` files on startup
(no override of shell-exported vars):

1. `./.env` — current working directory
2. parents of cwd, walked upward
3. `<binary_dir>/.env` — next to the installed binary

See [API_KEYS.md](API_KEYS.md) for step-by-step setup per provider —
where to sign up, what scope to grant, what's in the free tier, and
which env var to set. See [HINTS.md](HINTS.md) for known
gotchas (rate-limit cliffs, Cloudflare blocks, auth landmines).

## See also

- [INSTALL.md](INSTALL.md) — full install guide for all four channels
- [API_KEYS.md](API_KEYS.md) — per-provider auth setup
- [HINTS.md](HINTS.md) — operator-grade "things that surprise you"
- [CLAUDE.md](CLAUDE.md) — repo conventions for contributors
