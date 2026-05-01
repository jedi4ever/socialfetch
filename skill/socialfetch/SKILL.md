---
name: socialfetch
description: Fetch content from social-media URLs (HackerNews, Reddit, GitHub, X/Twitter, RSS, Medium, Substack, generic articles) and run web/social searches (DuckDuckGo, Bing, SerpAPI, Tavily, X, HN) — output as clean markdown or structured JSON. Use whenever the user asks to "pull", "fetch", "download", "summarise", or "search the web/Twitter/HN" for content at a URL or query.
---

# socialfetch skill

Wraps the `socialfetch` Go binary at `scripts/socialfetch` (relative to this skill). Always invoke that binary — do not reimplement fetching yourself.

## Two subcommands

```
scripts/socialfetch fetch  <url> [<url>...]   [flags]
scripts/socialfetch search "<query>"          [flags]
```

Run `scripts/socialfetch --help` for the full reference. Output defaults to **markdown**; pass `-f json` or `-f jsonl` for structured input to other tools.

## Decision rules

- **One URL → fetch it.** `scripts/socialfetch fetch <url>` auto-detects the source from the host (HN, Reddit, GitHub, X, RSS, or generic article).
- **A list of URLs → batch.** Pipe via stdin (`cat urls.txt | scripts/socialfetch fetch`) or use `-i FILE`. Add `-j 8` for parallel fetches; output stays in input order.
- **Save to disk →** `-o FILE` for one file, `-o DIR/` for one file per URL.
- **A query → search.** Pick the provider that matches the user's intent:
  - "search the web" / unspecified → `duckduckgo` (no auth)
  - "high-quality web search for AI agents" → `tavily` (needs `TAVILY_API_KEY`)
  - "search HN" → `hackernews`
  - "search Twitter/X" → `x` (needs `X_API_KEY` + `X_API_SECRET`)
  - "search via Google" → `serpapi` (needs `SERPAPI_KEY`)
  - "search Bing" → `bing` (needs `BING_API_KEY`)

## Flags worth remembering

| flag | when |
| -- | -- |
| `-f markdown\|json\|jsonl` | format (default markdown) |
| `-o PATH` | stdout / FILE / DIR/ |
| `-i FILE` | URLs file (`-` = stdin; auto-detected when piped) |
| `-j N` | parallel workers for batch fetch |
| `--no-comments` | skip comment trees on HN/Reddit |
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

## When NOT to use this skill

- The user wants to **post** content (this skill only reads).
- The URL is behind a paywall/login — output will be the gated stub. Tell the user.
- The URL needs a logged-in browser session (LinkedIn, X home feed, etc.) — not supported.
