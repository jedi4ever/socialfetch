# socialfetch

A small Go CLI for pulling URLs from social/news sources — HackerNews, Reddit, GitHub, Twitter/X, LinkedIn, Medium/Substack, RSS feeds, blog posts — and rendering them as **clean Markdown** or **structured JSON / JSONL**.

It also has a `search` subcommand for running queries against DuckDuckGo or SerpAPI.

```bash
$ socialfetch fetch https://news.ycombinator.com/item?id=43000000
$ socialfetch fetch -i bookmarks.txt -o ./out/ -f json
$ socialfetch search "vercel ai sdk" -p duckduckgo -n 5
```

## Install / build

```bash
make build             # builds ./dist/socialfetch
make install           # go install into $GOBIN
```

Requires Go 1.25+. The only third-party dependency is `golang.org/x/net/html`.

## Usage

```
socialfetch fetch  <url> [<url>...] [flags]
socialfetch search "<query>" [flags]
socialfetch list                      # list fetch + search providers
socialfetch help [fetch|search]
```

Run `socialfetch help fetch` or `socialfetch help search` for the full flag reference. Help output is written to be parseable by agents — every flag has a short and long form and lists its accepted values.

### Fetch

| flag | meaning |
| -- | -- |
| `-f, --format` | `markdown` (default), `json`, `jsonl` |
| `-o, --output` | `-` or unset for stdout, `FILE` for a single file, `DIR/` for one file per URL |
| `-i, --input`  | read URLs from FILE (one per line; `-` = stdin; `#` lines are comments). Auto-detected when stdin is a pipe. |
| `-j, --jobs N` | parallel fetch workers (default 4). Output stays in input order even with concurrency. |
| `-l, --log`    | audit/debug log destination (`-` or `stderr` for stderr) |
| `--no-comments` / `--comments` | skip / include comment trees (default include) |
| `--max-comments N` | cap total comments per item |
| `--timeout DUR` | overall timeout (default `60s`) |

When you pass multiple URLs and `-f json`, the format is automatically promoted to `jsonl` so consumers see one item per line. Pipe a list of URLs in directly with `cat urls.txt | socialfetch fetch -f jsonl` — no `-i` needed.

### Search

| flag | meaning |
| -- | -- |
| `-p, --provider` | `duckduckgo` (default), `brave`, `serpapi`, `tavily`, `perplexity`, `x`, `youtube`, `bluesky`, `arxiv`, `hackernews`, or `linkedin` (bridge + login required, use sparingly — rate limits) |
| `-n, --max` | max results (default 10) |
| `-f, --format` | `markdown` (default), `json`, or `jsonl` |
| `-o, --output` | stdout or file |
| `-l, --log` | audit log destination |

| provider | auth |
| -- | -- |
| `duckduckgo` | none (scrapes the lite endpoint) |
| `brave`      | `BRAVE_API_KEY` (free tier 2,000 q/mo; supports native `--last 7d` via the `freshness` parameter) |
| `serpapi`    | `SERPAPI_KEY` |
| `tavily`     | `TAVILY_API_KEY` (AI-tuned, scored, supports domain include/exclude) |
| `x`          | `X_API_KEY` + `X_API_SECRET` (X v2 recent search, last 7 days) |
| `youtube`    | `YOUTUBE_API_KEY` (Data API v3 search.list — 100 units per call, ~100 searches/day on free tier; supports native `--last 7d` / `--after` filters) |
| `bluesky`    | none (public AppView search; native `since`/`until` for date filters) |
| `arxiv`      | none (Atom search API; results client-side filtered by `--last 7d` / `--after` since the API has no date param) |
| `hackernews` | none (Algolia public search) |

When `X_API_KEY` + `X_API_SECRET` are set, the `twitter` fetch source automatically prefers X's official v2 API over the syndication endpoint, picking up long-form `note_tweet` content for tweets over 280 characters.

## Credentials

All API keys are **optional** — features gated on a missing key degrade gracefully. The binary auto-loads `.env` files on startup (no override of shell-exported vars):

1. `./.env` — current working directory
2. `<binary_dir>/.env` — next to the installed binary (typically `~/.claude/skills/socialfetch/.env`)

**See [API_KEYS.md](./API_KEYS.md)** for step-by-step setup instructions per provider — where to sign up, what scope to grant, what's in the free tier, and which env var to set.

## Sources

| source | example URL | notes |
| -- | -- | -- |
| `hackernews` | `https://news.ycombinator.com/item?id=NN` or bare ID | uses the public Firebase API |
| `reddit` | `https://www.reddit.com/r/<sub>/comments/<id>/<slug>/` | uses Reddit's `.json` endpoint, no auth |
| `github` | `https://github.com/<owner>/<repo>` | uses the GitHub REST API; honors `GITHUB_TOKEN` |
| `twitter` | `https://x.com/<user>/status/<id>` | syndication endpoint by default; X v2 API + replies when `X_API_KEY`/`X_API_SECRET` are set |
| `linkedin` | `linkedin.com/posts/...`, `/feed/update/...`, `/in/<user>`, `/pulse/...` | requires the browser bridge — see below |
| `youtube` | `youtube.com/watch?v=...`, `youtu.be/...`, `/shorts/...`, `/live/...`, `/embed/...` | metadata + transcript via scraping; transcript provider configurable (see below); comments via Data API v3 when `YOUTUBE_API_KEY` is set |
| `bluesky` | `bsky.app/profile/<handle-or-did>/post/<rkey>` | resolves handle → DID, fetches the post + nested reply thread via the public AppView (no auth) |
| `arxiv` | `arxiv.org/abs/<id>`, `/pdf/<id>`, `/html/<id>` | metadata + abstract + categories via the export API (no auth) |
| `medium` | `medium.com` / `*.medium.com` | bridge-first (paywall-aware via your logged-in session); HTTP fallback for public excerpts |
| `substack` | `*.substack.com` | bridge-first (member-only posts) with HTTP fallback |
| `rss` | any URL whose path mentions `/feed`, `/rss`, `/atom` or ends in `.xml` | parses both RSS 2.0 and Atom |
| `article` | any other `http(s)` URL | catch-all: extracts OpenGraph / JSON-LD / article body and converts to markdown |

## YouTube transcripts

Three transcript backends, switchable via `YOUTUBE_TRANSCRIPT_PROVIDER`:

| provider | how | trade-offs |
| -- | -- | -- |
| `ytdlp` | shells out to [yt-dlp](https://github.com/yt-dlp/yt-dlp) | **Most reliable.** Updated weekly; handles YouTube's anti-scraping cat-and-mouse (PoToken, age gates, region locks). Adds a runtime dep — install with `brew install yt-dlp` or `pip install yt-dlp`. |
| `innertube` | pure Go; scrapes the watch page for the transcript continuation token, then POSTs to `youtubei/v1/get_transcript` | **No extra runtime dep**, single binary. Uses YouTube's private/internal API (the same one youtube.com itself calls), so it can break without notice when YouTube changes their schema. Some videos return `FAILED_PRECONDITION` even when transcripts exist. |
| `kkdai` | `github.com/kkdai/youtube/v2`'s caption-track endpoint | The legacy timedtext URL; YouTube has been gating it with HTTP 400 throughout 2026. Useful as a last fallback. |

`auto` (default) tries them in order yt-dlp → InnerTube → kkdai and uses the first success. Note that **YouTube's official Data API v3 cannot fetch transcripts** for videos you don't own — `captions.download` requires OAuth as the channel owner — which is why all three providers fall back on scraping or unofficial endpoints.

## Browser bridge

LinkedIn (and the paywalled paths of Medium / Substack) need an authenticated browser session, so socialfetch ships a small Chrome MV3 extension at `chrome-extension/` and a local WebSocket bridge that brokers between the CLI and your real, logged-in browser.

```bash
# one-time: load chrome-extension/ in chrome://extensions (Developer mode → Load unpacked)
socialfetch bridge start         # daemonize on :5555
socialfetch bridge status        # 0 connected, 1 no extension, 2 not running
socialfetch bridge stop          # SIGTERM the daemon
```

Once `bridge status` reports `connected`, fetching a LinkedIn / Medium / Substack URL routes through your browser; everything else still goes direct over HTTP.

## Output

Every output — JSON or markdown — includes both `fetched_at` (when the data was pulled) and `written_at` (when this output was produced) plus author, source, score, tags, and comment trees where applicable. JSON output uses a stable `Envelope { written_at, item }` shape; JSONL emits one envelope per line.

## Project layout

```
cmd/socialfetch/        CLI entry point (subcommands, flags, batch, output routing)
internal/core/          shared types: Item, Comment, Media, Fetcher, HTTP helpers, audit log
internal/htmlmeta/      shared HTML metadata extractor (og:, JSON-LD, canonical, article body)
internal/htmlmd/        shared HTML→Markdown converter
internal/render/        JSON / JSONL / Markdown renderers
internal/search/        Search Provider interface + Registry
   duckduckgo/          lite-endpoint scraper, no auth
   serpapi/             SerpAPI client (gated on SERPAPI_KEY)
   tavily/              Tavily AI-tuned web search (gated on TAVILY_API_KEY)
   xsearch/             X v2 recent-search via OAuth2 app-only
internal/xauth/         shared X OAuth 2.0 App-Only token exchange + cache
internal/sources/       per-source fetchers
   hackernews/          Firebase API
   reddit/              .json endpoint
   github/              REST API
   twitter/             syndication endpoint
   rss/                 RSS / Atom XML
   article/             og: + JSON-LD + article body → markdown
```

The CLI consults fetchers in order and stops at the first match — specific sources first, with the article catch-all last.

## Claude skill

The repo also packages itself as a Claude Code skill at `skill/socialfetch/`. The `SKILL.md` there tells Claude when to invoke the binary; `make build` (and `make skill`) refresh `skill/socialfetch/scripts/socialfetch` so the bundled binary is always in sync with the source.

```bash
make skill-install           # copy SKILL.md + binary into ~/.claude/skills/socialfetch/
SKILL_INSTALL_DIR=./somewhere make skill-install   # or anywhere you want
```

After installing, prompts like *"fetch this HN thread"* or *"search Twitter for X"* will route through `socialfetch` instead of Claude's built-in WebFetch/WebSearch — giving you full structured comment trees, long-form X tweets, scored Tavily results, etc.

## Tests

```bash
make test          # offline unit tests; uses httptest servers, no network
make test-live     # live tests behind the //go:build live tag — hits real HN/GitHub/etc.
make test-cover    # offline tests with coverage
```

Live tests are guarded by the `live` build tag so the default `go test ./...` stays fast and deterministic.

## Adding a new source

1. Create `internal/sources/<name>/<name>.go` with a `Fetcher` that satisfies `core.Fetcher` (`Name`, `Match`, `Fetch(ctx, raw, opts)`).
2. Return a populated `*core.Item`. Use `core.GetJSON` / `core.GetBytes` for HTTP, and `htmlmeta.Parse` + `htmlmd.Convert` if you're scraping HTML.
3. Add an httptest-backed unit test next to it (`<name>_test.go`).
4. Register the new fetcher in `cmd/socialfetch/main.go`'s `buildRegistries()` — specific sources first, before the `article` catch-all.
5. Add a one-liner example in `exampleFor()` so it shows up in `socialfetch list`.
6. Optionally add a `live_test.go` behind `//go:build live`.

## Adding a new search provider

1. Create `internal/search/<name>/<name>.go` implementing the `search.Provider` interface (`Name`, `Search`).
2. Add a unit test with httptest fixtures.
3. Register it in `buildRegistries()`.
