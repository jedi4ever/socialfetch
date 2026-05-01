# socialfetch

A small Go CLI for pulling URLs from social/news sources — HackerNews, Reddit, GitHub, Twitter/X, RSS feeds, Medium/Substack/blog posts — and rendering them as **clean Markdown** or **structured JSON / JSONL**.

It also has a `search` subcommand for running queries against DuckDuckGo or SerpAPI.

```bash
$ socialfetch fetch https://news.ycombinator.com/item?id=43000000
$ socialfetch fetch -i bookmarks.txt -o ./out/ -f json
$ socialfetch search "vercel ai sdk" -p duckduckgo -n 5
```

## Install / build

```bash
make build             # builds ./bin/socialfetch
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
| `-i, --input`  | read URLs from FILE (one per line; `-` = stdin; `#` lines are comments) |
| `-l, --log`    | audit/debug log destination (`-` or `stderr` for stderr) |
| `--no-comments` / `--comments` | skip / include comment trees (default include) |
| `--max-comments N` | cap total comments per item |
| `--timeout DUR` | overall timeout (default `60s`) |

When you pass multiple URLs and `-f json`, the format is automatically promoted to `jsonl` so consumers see one item per line.

### Search

| flag | meaning |
| -- | -- |
| `-p, --provider` | `duckduckgo` (default) or `serpapi` |
| `-n, --max` | max results (default 10) |
| `-f, --format` | `json` (default), `jsonl`, or `markdown` |
| `-o, --output` | stdout or file |
| `-l, --log` | audit log destination |

`serpapi` requires the `SERPAPI_KEY` environment variable.

## Sources

| source | example URL | notes |
| -- | -- | -- |
| `hackernews` | `https://news.ycombinator.com/item?id=NN` or bare ID | uses the public Firebase API |
| `reddit` | `https://www.reddit.com/r/<sub>/comments/<id>/<slug>/` | uses Reddit's `.json` endpoint, no auth |
| `github` | `https://github.com/<owner>/<repo>` | uses the GitHub REST API; honors `GITHUB_TOKEN` |
| `twitter` | `https://x.com/<user>/status/<id>` | uses the public syndication endpoint |
| `rss` | any URL whose path mentions `/feed`, `/rss`, `/atom` or ends in `.xml` | parses both RSS 2.0 and Atom |
| `article` | any other `http(s)` URL | catch-all: extracts OpenGraph / JSON-LD / article body and converts to markdown |

LinkedIn is **not** included: it requires an authenticated browser session and doesn't fit the no-auth shape of the others.

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
internal/sources/       per-source fetchers
   hackernews/          Firebase API
   reddit/              .json endpoint
   github/              REST API
   twitter/             syndication endpoint
   rss/                 RSS / Atom XML
   article/             og: + JSON-LD + article body → markdown
```

The CLI consults fetchers in order and stops at the first match — specific sources first, with the article catch-all last.

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
