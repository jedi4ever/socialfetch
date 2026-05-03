# Installing social-skills

social-fetch ships in four flavors, all wrapping the same Go binary —
pick whichever matches your install style:

- **Option A — Claude Desktop Extension (.mcpb)**: drag-into-Settings
  installer with key prompts, secrets in OS keychain. Best UX for
  Claude Desktop users.
- **Option B — Remote MCP server**: `social-fetch mcp --ngrok` runs
  the protocol over HTTPS so cloud-hosted clients (claude.ai,
  Perplexity, Claude Code's `mcp add http`) can connect to your
  local binary. Keys stay in your `.env` / shell.
- **Option C — Skill**: file-based, drop SKILL.md + the binary into
  `~/.claude/skills/social-fetch/`, manage `.env` yourself. Works in
  Claude Desktop and Claude Code; no plan-tier gating.
- **Option D — Claude Code plugin (marketplace)**:
  `/plugin marketplace add jedi4ever/social-skills` then
  `/plugin install social-fetch`. Same skill content, distributed via
  Claude Code's plugin system. Requires the `social-fetch` binary on
  PATH separately.

## Option A — Claude Desktop Extension (.mcpb)

Recommended if you want one-click install with API-key prompts
stored in the macOS Keychain.

### Build

```bash
git clone https://github.com/jedi4ever/social-skills.git
cd social-skills
make claude-desktop-extension-package
```

Produces `dist/social-skills-claude-desktop-extension-<version>-darwin-arm64.mcpb`
(macOS Apple Silicon only at the moment — Phase 2 adds amd64 / Linux
/ Windows builds).

### Install (fastest path)

```bash
open dist/social-skills-claude-desktop-extension-0.2.0-darwin-arm64.mcpb
```

That hands the file off to Claude Desktop. Skip ahead to step 3 below.

### Install (manual)

1. **Open Claude Desktop** (`cmd+,` opens Settings).
2. **Settings → Extensions** in the sidebar. **Drag the `.mcpb`
   file** onto the Extensions panel, or double-click it in Finder
   (macOS hands it off to Claude Desktop).
3. Claude Desktop opens an **install dialog** with one form field
   per `user_config` entry — 19 fields total (15 API keys + 4
   routing hints). **Fill in only the keys you have**; everything is
   optional and the provider chains fall through gracefully on
   missing keys. Sensitive fields land in the macOS Keychain;
   routing hints (`HTML2MD_PROVIDER`, `HTML2MD_READER`,
   `TAVILY_TOPIC`) live in plain Claude Desktop config.
4. Click **Install**. Six MCP tools become available in any new
   conversation: `fetch`, `search`, `ask`, `timeline`,
   `list_providers`, `bridge_status`.
5. **Verify** — in a new conversation ask: *"use social-fetch to
   fetch https://news.ycombinator.com/item?id=1"*. Claude should
   call the `fetch` tool and return the parsed thread.

If the install dialog doesn't appear, your Claude Desktop build may
predate September 2025 (when the format renamed `.dxt` → `.mcpb`).
Update Claude Desktop and try again.

### Update / uninstall

- **Update (preserves API keys)**: `git pull && make claude-desktop-extension-package`,
  then drag the new `.mcpb` straight onto Settings → Extensions —
  **without** uninstalling first. Claude Desktop matches by manifest
  `name` and updates in place, keeping every keychain entry. The
  install dialog still appears but fields are pre-populated.
- **Uninstall**: Settings → Extensions → ⋯ menu next to social-fetch
  → **Remove**.

> ⚠️ **Don't uninstall before installing a new version** — uninstall
> wipes the keychain entries, so a subsequent drag presents an empty
> dialog and you have to re-enter every key. Just drag the new
> `.mcpb` over the old one to update in place.

### Validate the manifest

The `@anthropic-ai/mcpb` CLI is a local devDependency (see
`package.json`). `make claude-desktop-extension-package` already chains through
validation, but you can run it standalone:

```bash
make extension-validate
```

Or directly: `./node_modules/.bin/mcpb validate extensions/claude-desktop/manifest.json`.

---

# Option B — Remote MCP server (claude.ai, Perplexity, Claude Code)

Use this when you want social-fetch reachable from a **cloud-hosted
chat client** that connects to a remote MCP server over HTTPS rather
than launching a local binary. The same `social-fetch mcp` subcommand
runs the protocol over HTTP/Streamable instead of stdio; pair it with
ngrok during local development to get a public HTTPS URL without
standing up cloud infra.

## Quickest path (ngrok)

```bash
social-fetch mcp --ngrok                # defaults to :8080
social-fetch mcp --ngrok --http :9090   # override the port
```

Output looks like:

```
social-fetch mcp: bearer-token auth enabled (auto-generated (--ngrok))
social-fetch mcp: listening on :8080 (Streamable HTTP)

──────────────────────────────────────────────────────────────
  social-fetch MCP server is live via ngrok.

  URL:    https://abc-xyz.ngrok-free.dev/mcp
  Token:  bf8b008772dc29d78415cd5dc7e3693f5a191d6a831c2008ba909d39b0ebee2c

  Add to claude.ai → Settings → Connectors → Add custom connector:
    1. Connector URL:  https://abc-xyz.ngrok-free.dev/mcp
    2. Authentication:  Bearer token (paste the token above)

  Ctrl+C to stop the server and tear down the tunnel.
──────────────────────────────────────────────────────────────
```

Three things to know:

- **The URL changes between sessions on ngrok's free tier** (~8h max
  per session). Re-run `social-fetch mcp --ngrok` to get a new one.
- **API keys come from `.env` / shell env on YOUR machine** — same
  resolver as the local skill. Cloud clients never see them.
- **Bridge providers (LinkedIn / Medium / Substack) keep working**
  because the binary runs on your machine. Self-hosted on Fly.io
  etc. they wouldn't.

Verify the server is reachable before pasting into a connector UI:

```bash
curl https://abc-xyz.ngrok-free.dev/         # → JSON status, no auth needed
curl https://abc-xyz.ngrok-free.dev/health   # same
```

Tail incoming connections in another shell:

```bash
social-fetch monitor
# every probe lands as cmd=mcp:http with method+path+IP+status+duration
```

## Connect from claude.ai

Claude.ai's Custom Connector UI lives at **Settings → Connectors →
Add custom connector**.

> ⚠️ Custom Connectors are gated by plan tier and admin settings.
> Free / Pro accounts may not see the option yet (phased rollout).
> Team / Enterprise plans need a workspace admin to enable
> "Custom Connectors" in admin settings first.

If you can see the panel:

1. **Connector URL**: `https://abc-xyz.ngrok-free.dev/mcp` (the `/mcp`
   suffix is required — it's where the protocol handler lives).
2. **Authentication**: Bearer token. Paste the token printed by
   `--ngrok`. Claude.ai sends it as `Authorization: Bearer <token>`.
3. (Fallback) If the UI doesn't expose a bearer-auth field, paste a
   URL with the token embedded:
   `https://abc-xyz.ngrok-free.dev/mcp?token=<token>`. Same effect.

Save → claude.ai will preflight the URL. The seven social-fetch tools
(`social_fetch_fetch`, `_search`, `_ask`, `_timeline`, `_research`,
`_list_providers`, `_bridge_status`) appear in any new conversation.

## Connect from Perplexity

Perplexity's Pro and Enterprise tiers added Custom Connectors / MCP
integrations in 2025. As of early 2026, the UI is in
**Settings → Integrations → Connectors** (Pro consumer) or
**Admin → Connectors** (Enterprise).

> ⚠️ Like claude.ai, Connector availability depends on your plan.
> Free Perplexity accounts don't have it; Pro typically does. Comet
> (Perplexity's browser) has its own MCP integration accessed from
> the Assistant settings.

The fields you'll fill in match the claude.ai shape — Streamable HTTP
is a spec, not an Anthropic format. Paste:

1. **Server URL**: `https://abc-xyz.ngrok-free.dev/mcp`.
2. **Authentication header**: `Authorization: Bearer <token>` (Perplexity's
   UI typically asks for the header name + value separately; some
   variants ask just for an API key and assume the Bearer prefix).
3. **Description** (optional): "social-fetch — fetch / search / ask
   / research / LinkedIn timelines via the local browser bridge."

If Perplexity's UI rejects the URL with a generic "couldn't reach
server" error, run `curl https://abc-xyz.ngrok-free.dev/health` from
your phone (different network) to rule out connectivity. If `/health`
returns 200 from elsewhere on the internet, the issue is on
Perplexity's connector setup side, not your tunnel.

Tail `social-fetch monitor` while clicking "Save" in Perplexity's
connector UI — every probe lands in the audit log, so you'll see
exactly which method + path + auth header Perplexity sent (or
whether anything reached you at all).

## Connect from Claude Code (CLI)

```bash
claude mcp add social-fetch https://abc-xyz.ngrok-free.dev/mcp \
  --transport http \
  --header "Authorization: Bearer <token>"
```

Doesn't depend on plan tier — Claude Code's MCP support is
universal. The seven tools become available in every session.

## When you're ready for 24/7 uptime

ngrok is meant for development. For something always-on, swap the
tunnel for a real host:

| host | shape |
|---|---|
| **Fly.io** | `fly launch` from the repo, set secrets via `fly secrets set ANTHROPIC_API_KEY=...`, deploy. Same `--http :8080` binary, different supervisor. |
| **Railway** | New project from GitHub repo, env vars in dashboard. |
| **Your VPS** | systemd unit running `social-fetch mcp --http :8080`, nginx in front for TLS. |

All three follow the same recipe: build a Linux binary
(`GOOS=linux GOARCH=amd64 go build`), run with `MCP_AUTH_TOKEN=...`
in the env, point your client at the public URL. The server side is
identical to local — only the host changes. **Bridge providers
(LinkedIn / Medium / Substack) stop working** in a remote deploy
because there's no Chrome on the host; everything else (fetch /
search / ask / timeline-X / research) works unchanged.

---

# Installing the `social-fetch` skill (Option C)

This guide walks through installing the bundled skill (`skills/social-fetch/`)
so it's discoverable by **Claude Desktop** and **Claude Code**. Both apps
read skills from the same location: `~/.claude/skills/<name>/`.

## At a glance

```bash
git clone https://github.com/jedi4ever/social-skills.git
cd social-skills
make skill-install                  # builds the binary and copies it +
                                    # SKILL.md to ~/.claude/skills/social-fetch/
```

That's it. Restart Claude Desktop and the skill appears in the available-skills
list when a relevant task comes up (fetch a URL, search the web, pull a
LinkedIn timeline, etc.).

## Prerequisites

- **Go 1.22+** — to build the binary (`go version` to check).
- **Claude Desktop** installed and signed in.
- **Optional:** Chrome (for the LinkedIn / Substack / Medium bridge — see below).
- **Optional:** any subset of the API keys listed in [API_KEYS.md](API_KEYS.md).
  Every key is optional; missing keys degrade gracefully.

## What `make skill-install` does

Inspect the target in `Makefile:50` if you're curious. In short:

1. Builds `dist/social-fetch` from `cmd/social-fetch` with `-ldflags="-s -w"
   -trimpath` (smaller, reproducible binary).
2. Copies the binary to `skills/social-fetch/scripts/social-fetch` (the bundled
   layout the skill expects).
3. Copies both `skills/social-fetch/SKILL.md` and the binary to
   `~/.claude/skills/social-fetch/` — the standard Anthropic skills directory.

Override the destination with `SKILL_INSTALL_DIR`:

```bash
make skill-install SKILL_INSTALL_DIR=/path/to/your/skills/social-fetch
```

## Verifying

After install, the directory should look like this:

```
~/.claude/skills/social-fetch/
├── SKILL.md             # frontmatter + usage docs Claude reads
└── scripts/
    └── social-fetch      # the Go binary
```

**In Claude Desktop:** start a new conversation and ask something like *"fetch
https://news.ycombinator.com/item?id=1"*. Claude should pick up the skill,
shell out to `scripts/social-fetch fetch <url>`, and return the rendered
markdown. If it doesn't, check that Claude Desktop is reading from
`~/.claude/skills/` (some early builds used a different path — see
**Troubleshooting** below).

**In Claude Code:** the same install works — Claude Code reads from the same
directory.

## API keys (optional)

Drop a `.env` file alongside the binary or in your working directory:

```bash
# Choose one location:
echo 'TAVILY_API_KEY=...' >> ~/.claude/skills/social-fetch/.env
# OR put it in any directory you launch Claude Desktop from.
```

The binary loads, in order, **without overriding shell env vars**, by
walking up from two starting points (up to 4 levels, stopping at
`$HOME` or filesystem root):

1. **Current working directory upward** — running from any subdir of
   a project with a root `.env` finds it (e.g. cwd
   `~/dev/myapp/scripts/` walks up to `~/dev/myapp/.env`).
2. **Binary location upward** — handles the skill install layout
   `~/.claude/skills/social-fetch/scripts/social-fetch` finding
   `~/.claude/skills/social-fetch/.env` one level up. The exact path
   `~/.claude/skills/social-fetch/.env` is what the install
   instructions below assume.

See [API_KEYS.md](API_KEYS.md) for the full list of supported keys and where
to obtain each one. Free tier coverage:

- **DuckDuckGo** search — no key needed.
- **HackerNews / Reddit / GitHub / arXiv / RSS / generic article** fetch — no
  key needed.
- **YouTube** metadata — no key needed; transcripts via `yt-dlp` if
  installed; comments need `YOUTUBE_API_KEY`.
- **Tavily / Brave / SerpAPI / Google (Gemini) / Perplexity /
  Grok / OpenAI / Anthropic / X / Bluesky** — each needs its own key
  (free tiers exist for most; OpenAI and Anthropic require paid
  accounts, no free tiers).

### Ask providers at a glance

| provider | env var | pricing |
|---|---|---|
| `perplexity` | `PERPLEXITY_API_KEY` (or `PPLX_API_KEY`) | pay-per-token; small payment method required |
| `grok` | `XAI_API_KEY` (or `GROK_API_KEY`) | pay-per-token + per-tool fee |
| `openai` | `OPENAI_API_KEY` | pay-per-token + per-tool fee; no free tier |
| `anthropic` | `ANTHROPIC_API_KEY` | pay-per-token + $10/1k searches; no free tier |
| `google` | `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) | free tier: 1,500 req/day on `gemini-flash-latest` |
| `tavily` | `TAVILY_API_KEY` | free: 1,000 searches/month |
| `serpapi` | `SERPAPI_KEY` | free: 100 searches/month |

See [API_KEYS.md](API_KEYS.md) for sign-up walkthroughs per provider.

## Bridge (LinkedIn, Medium, Substack paywalls, X bookmarks)

Three sources route through a local browser-extension bridge so they can
reuse your **already-logged-in browser session**:

- **LinkedIn** — required (no anonymous read path).
- **Medium / Substack** — optional; the bridge bypasses paywalls when
  available, falls back to direct HTTP otherwise.

### One-time setup

1. Open Chrome → `chrome://extensions/` → enable **Developer mode**.
2. Click **Load unpacked** → select the `extensions/chrome/` directory inside this
   repo.
3. The social-fetch extension icon appears in the toolbar.

### Per-session

```bash
social-fetch bridge start          # daemonize, write PID file
social-fetch bridge status         # 'connected' / 'not connected' / 'not running'
social-fetch bridge stop           # graceful SIGTERM
```

The bridge runs at `http://127.0.0.1:5555` by default. Override with
`--port N`. Once started, log into LinkedIn (etc.) in any Chrome tab — the
extension reconnects to the bridge every ~6 seconds.

## Updating

After pulling new commits:

```bash
git pull
make skill-install
```

The Makefile rebuilds the binary whenever any Go source file changes, so
re-running `skill-install` always installs the current tip.

## Uninstalling

```bash
make skill-clean        # removes ~/.claude/skills/social-fetch and ./bin
```

## Troubleshooting

**"Skill doesn't appear in Claude Desktop after install."**
- Restart Claude Desktop fully (quit + relaunch, not just close window).
- Confirm the install location: `ls -la ~/.claude/skills/social-fetch/`
  should show `SKILL.md` and `scripts/social-fetch`.
- Some Claude Desktop builds expect a slightly different path. If
  `~/.claude/skills/` doesn't work, check **Settings → Skills** for the
  configured directory and override with `SKILL_INSTALL_DIR=...`.

**"Skill is found but the binary fails to run."**
- Make sure the binary is executable: `chmod +x
  ~/.claude/skills/social-fetch/scripts/social-fetch`.
- If you see a "no such file" error on macOS, you may need to clear the
  Gatekeeper quarantine: `xattr -d com.apple.quarantine
  ~/.claude/skills/social-fetch/scripts/social-fetch`.

**"Bridge not running" when fetching a LinkedIn URL.**
- Run `social-fetch bridge status` to confirm.
- Run `social-fetch bridge start` if it's not running.
- Run `social-fetch bridge status` again — if it says *connected* you're
  set; if *not connected*, open Chrome with the extension loaded.

**"X search returns 0 results within the last 7 days."**
- This is the X v2 recent-search tier limit. The CLI pre-flight rejects
  windows older than 7 days with a clear message; if you need older,
  you'll need a paid X API tier.

## Where to ask questions

- File-level conventions: see [CLAUDE.md](CLAUDE.md) at the repo root.
- Feature requests / bugs: open an issue at
  https://github.com/jedi4ever/social-skills/issues.

---

# Option D — Claude Code plugin (marketplace)

For users who live in Claude Code (the CLI) rather than Claude Desktop,
social-fetch ships as a Claude Code **plugin** that wraps the same skill
markdown without an MCP server. One-line install via the plugin
marketplace:

```
/plugin marketplace add jedi4ever/social-skills
/plugin install social-fetch
```

The plugin lives at [`extensions/claude-code/`](extensions/claude-code/) in
this repo; the marketplace manifest is at
[`.claude-plugin/marketplace.json`](.claude-plugin/marketplace.json).

**Prerequisite:** the `social-fetch` binary must be on your PATH. The
plugin is purely the skill markdown + manifest — it does not bundle
the binary. Install once with `go install` or by downloading a
release:

```bash
go install github.com/jedi4ever/social-skills/cmd/social-fetch@latest
# or download from https://github.com/jedi4ever/social-skills/releases
social-fetch version    # confirm
```

For local development (testing changes before publishing), point
Claude Code at a working copy directly:

```bash
claude --plugin-dir ./extensions/claude-code
```

API keys come from your shell env or a nearby `.env` file — same as
every other distribution path. See [API_KEYS.md](API_KEYS.md).
