# social-skills

**Social-web skills for AI agents.**

Two binaries, one toolkit:

- **`social-fetch`** — pulls structured data from the social web
  and exposes it as CLI output, an MCP server, or a Claude skill.
- **`social-ledger`** — local SQLite + FTS5 store that auto-caches
  every fetch so agents can answer "have we seen this URL?" and
  "what did we save about X?" without re-fetching.

LLMs are great at understanding text but bad at getting it. The
social web — HackerNews threads, Reddit comments, GitHub repos,
X/Twitter posts, LinkedIn timelines, YouTube transcripts, Bluesky
feeds, arXiv papers, Medium / Substack articles, RSS feeds, generic
blog posts — lives behind a different API, DOM scraper, auth flow,
and rate limit per platform. **social-skills** hides all of that
behind one consistent interface and gives the agent **clean Markdown
or structured JSON** an LLM can actually parse.

Same shape covers grounded web search (Perplexity, Tavily, Brave,
SerpAPI, Google, DuckDuckGo) and grounded answer engines (Grok,
OpenAI, Anthropic, Gemini), and exposes everything as MCP tools so
**Claude Desktop, Claude Code, claude.ai, and Perplexity** can call
into it as a first-class tool — not as another `WebFetch` round-trip
that returns rendered HTML.

> **Also great as a plain CLI.** Every agent capability is also a
> shell command — `social-fetch search "vercel ai sdk" -p auto`,
> `social-ledger search "harness engineering"`, `social-fetch ask
> "what changed in Go 1.27?" -p perplexity`. Pipe into `jq`,
> redirect into files, embed in scripts. Agents are the primary
> audience, but humans get the same toolbox.

```bash
social-fetch fetch    https://news.ycombinator.com/item?id=43000000
social-fetch search   "vercel ai sdk" -p auto -n 10 --last 7d
social-fetch ask      "what changed in Go 1.27?" -p perplexity
social-fetch timeline @jedi4ever -p x --last 24h
social-fetch research "tessl harness engineering" -p anthropic
social-ledger search  "harness engineering"
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
- **MCP server built in.** `social-fetch mcp` exposes every CLI
  capability as Model Context Protocol tools, runnable over stdio
  (Claude Desktop) or Streamable HTTP (claude.ai, Perplexity, Claude
  Code remote-MCP). Same binary is your CLI and your MCP server.
- **Browser bridge** for authenticated paths — LinkedIn, Medium /
  Substack paywalls — via a small Chrome MV3 extension that brokers
  between the agent and your real, logged-in browser. Public content
  still goes direct over HTTP. The bridge is intentionally
  single-lane (one in-process mutex serializing all calls, plus
  randomized human-paced pauses between scrolls) so it stays under
  bot-detection thresholds — at the cost of not scaling for bulk
  scraping.
- **Citations first.** Every result carries `source`, `url`,
  `fetched_at`, `written_at`, scores, comment trees — so the agent
  can ground its answer in something the user can click back to.

## Install

social-fetch is built to plug into AI agents — pick whichever channel
matches the agent you're working with. Full step-by-step in
[INSTALL.md](INSTALL.md).

### 1. Claude Desktop Extension (`.mcpb`) — recommended

One-click install with API-key prompts that go straight into the
macOS Keychain. Best UX if you live in Claude Desktop.

```
1. Download social-skills-claude-desktop-extension-<version>-darwin-arm64.mcpb
   from https://github.com/jedi4ever/social-skills/releases/latest
2. Double-click the .mcpb (or drag it onto Claude Desktop →
   Settings → Extensions).
3. Fill in whichever API keys you have — every key is optional.
```

### 2. Claude Code plugin (marketplace)

One-line install if you use Claude Code:

```
/plugin marketplace add jedi4ever/social-skills
/plugin install social-fetch
```

Requires the `social-fetch` binary on your PATH separately (the
plugin is the skill markdown + manifest, not the binary).

### 3. Skill (file-based, Claude Desktop or Claude Code)

`SKILL.md` + binary dropped into `~/.claude/skills/social-fetch/`.
Useful when you want to manage `.env` yourself or work offline.

**Via npx (no clone, single command)** — using
[`vercel-labs/skills`](https://github.com/vercel-labs/skills),
the most-used skill installer (works with Claude Code, Claude
Desktop, OpenCode, Codex, and others):

```bash
npx skills add jedi4ever/social-skills --skill social-fetch
```

The `social-fetch` binary still needs to be on PATH separately — `npx
skills` only installs the markdown skill, not the binary.

**Via clone + make** — same end state, plus builds the binary:

```bash
git clone https://github.com/jedi4ever/social-skills.git
cd social-skills
make skill-install
```

Other community installers ([`claude-plugins`](https://github.com/Kamalnrf/claude-plugins),
[`agent-skills-cli`](https://github.com/alirezarezvani/claude-skills),
[`add-skill`](https://github.com/vercel-labs/skills)) work similarly
— they all read the same `SKILL.md` files from this repo.

### 4. Remote MCP server (claude.ai, Perplexity, Claude Code)

`social-fetch mcp` runs the MCP protocol over Streamable HTTP so
cloud-hosted clients can reach your local binary:

```bash
# Plain HTTP listener — bring-your-own-tunnel
MCP_AUTH_TOKEN=$(openssl rand -hex 32) \
  social-fetch mcp --http :8080
# expose :8080 publicly with whatever you prefer:
# Cloudflare Tunnel, Tailscale Funnel, fly.io machine, your own
# reverse proxy, etc. Then paste the resulting HTTPS URL +
# token into the client.
```

If you want a one-line public URL without configuring a tunnel
yourself, `--ngrok` spawns ngrok for you and prints the URL + an
auto-generated bearer token (requires `ngrok` on PATH and one-time
`ngrok config add-authtoken …`):

```bash
social-fetch mcp --ngrok           # convenience: prints URL + token
```

Either way, paste the URL + token into Settings → Connectors /
Custom Connector in claude.ai, Perplexity Pro, or
`claude mcp add http <url> --header "Authorization: Bearer <token>"`.

API keys stay in your local `.env` — never sent over the wire.

---

### Bare CLI (shell scripts, library use)

If you don't need any of the above and just want the binary:

```bash
go install github.com/jedi4ever/social-skills/cmd/social-fetch@latest
# or download a release tarball from
# https://github.com/jedi4ever/social-skills/releases — files like:
#   social-skills-<version>-darwin-arm64.tar.gz
#   social-skills-<version>-darwin-amd64.tar.gz
#   social-skills-<version>-linux-amd64.tar.gz
```

Build from source:

```bash
git clone https://github.com/jedi4ever/social-skills.git
cd social-skills && make build       # → ./dist/social-fetch
```

Requires Go 1.26+. Windows is not currently supported (the bridge
daemon uses Unix-only syscalls — run via WSL).

## Social platforms supported

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
| `gemini` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` | Gemini API with built-in `google_search` grounding |
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
social-fetch fetch     <url> [<url>…]      pull URL(s) into structured Item(s)
social-fetch search    "<query>"           run a search query
social-fetch ask       "<question>"        ask a grounded answer engine
social-fetch timeline  <user-or-url>       recent activity for a user
social-fetch research  "<question>"        EXPERIMENTAL multi-angle research
social-fetch mcp       [flags]             run as an MCP server (stdio or HTTP)
social-fetch bridge    {start|stop|status|run}    Chrome browser-bridge daemon
social-fetch monitor   [--tail N]          tail the global audit log
social-fetch list                          list every fetch / search / ask / timeline provider
social-fetch help      [subcommand]        full flag reference per subcommand
social-fetch version                       print the version
```

`social-fetch help <subcommand>` is the authoritative flag reference —
every flag has a short and long form; output is shaped to be parseable
by agents.

### `fetch`

```
social-fetch fetch <url>... [-f markdown|json|jsonl] [-o -|FILE|DIR/]
                           [-i FILE|-] [-j N] [--no-comments]
                           [--max-comments N] [--timeout DUR] [-l -|FILE]
```

Multiple URLs + `-f json` auto-promotes to `jsonl` (one item per
line). Stdin auto-detected when piped: `cat urls.txt | social-fetch fetch`.
`-j N` keeps results in input order despite concurrency.

### `search`

```
social-fetch search "<query>" [-p auto|<provider>|<chain>] [-n N]
                             [--last 7d|--after YYYY-MM-DD|--before …]
                             [--site DOMAIN[,…]] [-f markdown|json|jsonl]
```

Date filters are native where the provider supports them and
client-side otherwise. See HINTS.md for per-provider date-filter
quirks.

### `ask`

```
social-fetch ask "<question>" [-p auto|<provider>|<chain>] [--model NAME]
                             [--recency day|week|month|year]
                             [--max-tokens N] [--instructions "…"]
```

`--recency` is honored where the upstream supports it; `--instructions`
is the system-prompt-style preamble (perplexity, grok, openai,
anthropic, google).

### `timeline`

```
social-fetch timeline <handle-or-url> [-p x|linkedin]
                                     [--kind all|tweets|replies|retweets|posts|comments|reactions]
                                     [--last 7d] [-n N] [--expand] [--no-reshares]
```

Auto-detects the platform from the URL or `@-prefix`. `--expand`
deep-fetches each post (LinkedIn — slower but fuller content).

### `research` (experimental)

```
social-fetch research "<question>" [--orchestrator <ask-provider>]
                                  [--max-angles N] [-j N] [--json]
```

Decomposes the question into 3-8 angles, fans out parallel
fetch/search/ask/timeline calls, and synthesizes a markdown answer
with citations. Costs ~2 LLM calls + N tool calls per question. Use
when you'd otherwise issue 4-8 manual queries.

### `mcp`

```
social-fetch mcp                       # stdio (Claude Desktop Extension)
social-fetch mcp --http :PORT          # Streamable HTTP (claude.ai, ngrok)
social-fetch mcp --ngrok               # spawn ngrok automatically
```

Exposes `social_fetch_fetch`, `_search`, `_ask`, `_timeline`,
`_research`, `_list_providers`, `_bridge_status`, `_read_file` plus
the seven `social_ledger_*` tools as MCP tools. Set `MCP_AUTH_TOKEN`
for HTTP mode (auto-generated when `--ngrok` is combined with no env
var). HTTP-mode tee's tool calls + outbound platform HTTP traffic to
stderr live.

**Output is file-based by default** for the big-payload tools
(`_fetch`, `_ask`, `_research`, `_ledger_get`). The tool result is a
small JSON envelope containing metadata + a `content_file` path
pointing at a temp file with the rendered markdown body. Read the
body with the agent's built-in Read tool (Claude Code), or with
`social_fetch_read_file` (Claude Desktop / any MCP-only client). This
keeps article bodies, research reports, and grounded answers off the
JSON-RPC encode path — much faster than streaming 50 KB of escaped
markdown through a tool result. Pass `inline: true` on the producing
tool to get the body inline instead.

The `_fetch` envelope adds a **`hint`** field when extraction looks
suspiciously thin (small body from an `article`-source page —
typically a JS-rendered SPA), nudging the agent to retry via the
browser bridge or `SOCIAL_FETCH_CHAIN_ARTICLE=jina`.

The `_ledger_get` envelope adds a **`provenance`** field —
`auto-fetched` when the entry came in via `social_fetch_*` (we
extracted it ourselves), `agent-recorded` when stored via
`social_ledger_record` (content came from Claude WebFetch / curl /
hand-paste). Use it to weigh how much to trust a cached body before
quoting from it.

### `bridge`

```
social-fetch bridge start         # daemonize on :5555
social-fetch bridge status        # exit 0 connected / 1 not connected / 2 not running
social-fetch bridge stop          # SIGTERM
social-fetch bridge run           # foreground (no daemon)
```

One-time setup: load `extensions/chrome/` in `chrome://extensions/`
(Developer mode → Load unpacked). Required for LinkedIn, Medium /
Substack paywall fetches.

### `monitor`

```
social-fetch monitor                  # follow ~/Library/Caches/social-fetch/audit.jsonl
social-fetch monitor --tail 200       # last N lines then follow
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

## HTML → Markdown (and Jina Reader fallback)

For the catch-all `article` fetcher and any HTML-rendering source,
two knobs control how the page becomes markdown the agent can read:

### Converter (`HTML2MD_PROVIDER`)

| value | how |
|---|---|
| `kaufmann` (default) | library-backed converter — handles tables, code blocks, nested lists, and inline formatting that the older converter would flatten. |
| `builtin` | legacy hand-rolled walker — kept as a fallback for the rare case where `kaufmann` mis-renders something. |

### Per-platform fetch chain (`SOCIAL_FETCH_CHAIN_<PLATFORM>`)

Every fetcher walks a configurable chain of transport methods
(`bridge`, `http`, `headless`, `jina`, `api`, `syndication`).
Operators override per-platform via the env var; defaults live in
the platform Go code (`internal/platforms/*/fetch.go`'s
`defaultChain` var) so they don't drift.

The table below mirrors the code at the time of writing — **if
they ever disagree, the code wins**. Run `social-fetch hints
<platform>` for the canonical per-platform recipe (when to
override, transport-specific quirks).

| platform | default chain | env var |
|---|---|---|
| article (catch-all) | `headless,http,bridge,jina` | `SOCIAL_FETCH_CHAIN_ARTICLE` |
| linkedin | `headless,bridge,jina` | `SOCIAL_FETCH_CHAIN_LINKEDIN` |
| medium | `bridge,http,headless,jina` | `SOCIAL_FETCH_CHAIN_MEDIUM` |
| substack | `bridge,http,headless,jina` | `SOCIAL_FETCH_CHAIN_SUBSTACK` |
| twitter | `api,syndication,jina` | `SOCIAL_FETCH_CHAIN_TWITTER` |
| arxiv | `api,jina` | `SOCIAL_FETCH_CHAIN_ARXIV` |
| hackernews / reddit / github / youtube / bluesky / rss | single-method (`api` or `http`) | `SOCIAL_FETCH_CHAIN_<NAME>` |

Transport quick-reference:

- **`bridge`** — your real, logged-in Chrome via the
  social-fetch extension. Required for auth-walled content
  (LinkedIn comments, Medium / Substack member-only posts).
  Needs `social-fetch bridge start` + extension loaded.
- **`headless`** — local stealth Chromium via chromedp. Anonymous
  but JS-rendering. Faster with the daemon (see "Headless browser
  pool" below); falls back to per-call spawn when daemon's down.
- **`http`** — plain HTTP GET. Fastest, no JS. Some sites
  return a JS shell to non-browsers — those need `headless`.
- **`jina`** — `r.jina.ai/<url>` remote service. Anti-CF +
  headless rendering on Jina's infrastructure. Free tier has
  rate limits; set `JINA_API_KEY` for paid.
- **`api`** / **`syndication`** — platform's structured API
  (Twitter v2, HN Algolia, etc.) where applicable.

Examples:

```bash
# air-gapped — never reach for Jina
SOCIAL_FETCH_CHAIN_ARTICLE=http,bridge,headless

# always anonymous LinkedIn (skip the bridge)
SOCIAL_FETCH_CHAIN_LINKEDIN=headless,jina

# bypass Twitter v2 even when keys are set
SOCIAL_FETCH_CHAIN_TWITTER=syndication,jina
```

Set `JINA_API_KEY` if any chain includes `jina` and you want
higher rate limits than the free tier.

#### Deprecated: `HTML2MD_READER=jina`

Pre-dates the per-platform chain. Equivalent today:
`SOCIAL_FETCH_CHAIN_ARTICLE=jina,http,bridge`. Still honoured for
one release with a deprecation line in the audit log; will be
removed after.

## Browser bridge (LinkedIn, Medium / Substack paywalls)

LinkedIn (and the member-only paths of Medium / Substack) only
return useful content to an authenticated session. social-fetch's
answer is a small Chrome MV3 extension at `extensions/chrome/` that
opens a WebSocket to a local daemon (`social-fetch bridge run`) and
brokers requests through your real, logged-in browser tab. Public
content keeps going direct over HTTP — the bridge is only used
when a fetcher explicitly opts in.

```bash
# one-time: load extensions/chrome/ in chrome://extensions/
#   (Developer mode → Load unpacked → pick the directory)
social-fetch bridge start         # daemonize on :5555
social-fetch bridge status        # 0 connected / 1 not connected / 2 not running
social-fetch bridge stop          # SIGTERM the daemon
```

## Headless browser pool (anonymous JS-rendered fetches)

Separate from the bridge. `social-fetch headless start` daemonises
a pool of warm Chromium browsers via chromedp — anonymous (no
session reuse, never touches your real Chrome profile), fast
(~3s warm-tab vs ~5-6s cold-spawn), used by article / LinkedIn /
Medium / Substack chains when `headless` is in the chain.

```bash
social-fetch headless start [--pool 2] [--recycle 50]
social-fetch headless status     # one-shot pool snapshot
social-fetch headless monitor    # live-tailing TUI view (Ctrl-C exits)
social-fetch headless stop
```

When the daemon's down, the headless transport falls back to
per-call spawn — fetches still work, just slower. Default chains
already include `headless`; for the article fetcher it's the
preferred path. See `social-fetch headless --help` for full flag
list and the env vars (`SOCIAL_FETCH_HEADLESS_*`).

> **Permissions model — narrow by default, broad on opt-in.**
>
> The extension's static `host_permissions` cover only the named
> social sites: `linkedin.com`, `x.com` / `twitter.com`,
> `medium.com`, `substack.com`. Those are what the install dialog
> asks for; nothing else.
>
> If you want to fetch arbitrary HTTPS pages through your
> authenticated browser (Reddit, GitHub, Notion, …), open the
> extension popup → **Site permissions** → toggle **"Allow on all
> sites"**. That triggers Chrome's standard runtime-permission
> prompt and grants `https://*/*` until you toggle it off again
> (or remove it from `chrome://extensions/`). The toggle is
> reflected back from Chrome's permission state, so an external
> revoke shows up the next time you open the popup.
>
> Without the broad toggle on, asking the bridge to fetch a
> non-listed site fails fast with a `permission_required` error
> — the daemon surfaces it to the CLI so you know exactly what to
> click.
>
> Other practical bits:
>
> - The daemon listens on `127.0.0.1:5555` only — not exposed to
>   the network. Anything running locally that can reach that port
>   can still drive the extension, so treat the bridge like any
>   other localhost dev service.
> - Source is in `extensions/chrome/` (~10 small files); audit
>   `background.js` + `content.js` if you'd like to see the
>   actual surface.
> - Toggle the extension off in `chrome://extensions/` when you're
>   not using social-fetch and the host permissions stop applying
>   entirely.

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

## License & disclaimer

Released under the [MIT License](LICENSE) — free to use, modify, and
redistribute. The license itself includes the standard "as is, no
warranty, no liability" clauses; what follows is an explicit
plain-language version of those for the AI-specific bits.

> **social-fetch is plumbing for AI agents — and AI agents make
> mistakes.** This tool fetches third-party content (HackerNews,
> Reddit, X, LinkedIn, articles, …) and routes it through LLMs
> (Perplexity, OpenAI, Anthropic, Gemini, Grok, …) that can
> hallucinate, misattribute, paraphrase incorrectly, and surface
> outdated information. Every answer in this stack is a best-effort
> synthesis, not a verified fact.
>
> **Things you should NOT do** based solely on social-fetch output,
> without independent verification:
>
> - make legal, medical, or financial decisions
> - quote the output as if it were the source
> - act on factual claims that aren't covered by the citations
>   social-fetch returns alongside the answer
> - assume a missing citation means the claim is unsupported (or
>   the inverse — that a citation means the claim is correct)
>
> The `source` / `url` / `fetched_at` metadata on every result
> exists precisely so you can click back to the original. Do that
> for anything you care about. Treat social-fetch like a research
> assistant who's read a lot but might be wrong about any specific
> detail — useful for breadth, not authoritative on accuracy.
>
> The author and contributors accept no liability for decisions
> made on the basis of output from this tool, third-party API
> responses it relays, or downstream agents that consume it.

## See also

- [INSTALL.md](INSTALL.md) — full install guide for all four channels
- [API_KEYS.md](API_KEYS.md) — per-provider auth setup
- [HINTS.md](HINTS.md) — operator-grade "things that surprise you"
- [CLAUDE.md](CLAUDE.md) — repo conventions for contributors
- [LICENSE](LICENSE) — MIT
