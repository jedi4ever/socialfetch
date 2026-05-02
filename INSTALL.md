# Installing the `socialfetch` skill

This guide walks through installing the bundled skill (`skill/socialfetch/`)
so it's discoverable by **Claude Desktop** and **Claude Code**. Both apps
read skills from the same location: `~/.claude/skills/<name>/`.

## At a glance

```bash
git clone https://github.com/patrickdebois/social-skills.git
cd social-skills
make skill-install                  # builds the binary and copies it +
                                    # SKILL.md to ~/.claude/skills/socialfetch/
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

1. Builds `bin/socialfetch` from `cmd/socialfetch` with `-ldflags="-s -w"
   -trimpath` (smaller, reproducible binary).
2. Copies the binary to `skill/socialfetch/scripts/socialfetch` (the bundled
   layout the skill expects).
3. Copies both `skill/socialfetch/SKILL.md` and the binary to
   `~/.claude/skills/socialfetch/` — the standard Anthropic skills directory.

Override the destination with `SKILL_INSTALL_DIR`:

```bash
make skill-install SKILL_INSTALL_DIR=/path/to/your/skills/socialfetch
```

## Verifying

After install, the directory should look like this:

```
~/.claude/skills/socialfetch/
├── SKILL.md             # frontmatter + usage docs Claude reads
└── scripts/
    └── socialfetch      # the Go binary
```

**In Claude Desktop:** start a new conversation and ask something like *"fetch
https://news.ycombinator.com/item?id=1"*. Claude should pick up the skill,
shell out to `scripts/socialfetch fetch <url>`, and return the rendered
markdown. If it doesn't, check that Claude Desktop is reading from
`~/.claude/skills/` (some early builds used a different path — see
**Troubleshooting** below).

**In Claude Code:** the same install works — Claude Code reads from the same
directory.

## API keys (optional)

Drop a `.env` file alongside the binary or in your working directory:

```bash
# Choose one location:
echo 'TAVILY_API_KEY=...' >> ~/.claude/skills/socialfetch/.env
# OR put it in any directory you launch Claude Desktop from.
```

The binary loads, in order, **without overriding shell env vars**:

1. `./.env` (current working directory)
2. `<binary_dir>/.env` (next to the installed binary — typically
   `~/.claude/skills/socialfetch/.env`)

See [API_KEYS.md](API_KEYS.md) for the full list of supported keys and where
to obtain each one. Free tier coverage:

- **DuckDuckGo** search — no key needed.
- **HackerNews / Reddit / GitHub / arXiv / RSS / generic article** fetch — no
  key needed.
- **YouTube** metadata — no key needed; transcripts via `yt-dlp` if
  installed; comments need `YOUTUBE_API_KEY`.
- **Tavily / Brave / Bing / SerpAPI / Google (Gemini) / Perplexity /
  Grok / OpenAI / X / Bluesky** — each needs its own key (free tiers
  exist for most; OpenAI requires a paid account, no free tier).

### Ask providers at a glance

| provider | env var | pricing |
|---|---|---|
| `perplexity` | `PERPLEXITY_API_KEY` (or `PPLX_API_KEY`) | pay-per-token; small payment method required |
| `grok` | `XAI_API_KEY` (or `GROK_API_KEY`) | pay-per-token + per-tool fee |
| `openai` | `OPENAI_API_KEY` | pay-per-token + per-tool fee; no free tier |
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
2. Click **Load unpacked** → select the `extension/` directory inside this
   repo.
3. The PatAI extension icon appears in the toolbar.

### Per-session

```bash
socialfetch bridge start          # daemonize, write PID file
socialfetch bridge status         # 'connected' / 'not connected' / 'not running'
socialfetch bridge stop           # graceful SIGTERM
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
make skill-clean        # removes ~/.claude/skills/socialfetch and ./bin
```

## Troubleshooting

**"Skill doesn't appear in Claude Desktop after install."**
- Restart Claude Desktop fully (quit + relaunch, not just close window).
- Confirm the install location: `ls -la ~/.claude/skills/socialfetch/`
  should show `SKILL.md` and `scripts/socialfetch`.
- Some Claude Desktop builds expect a slightly different path. If
  `~/.claude/skills/` doesn't work, check **Settings → Skills** for the
  configured directory and override with `SKILL_INSTALL_DIR=...`.

**"Skill is found but the binary fails to run."**
- Make sure the binary is executable: `chmod +x
  ~/.claude/skills/socialfetch/scripts/socialfetch`.
- If you see a "no such file" error on macOS, you may need to clear the
  Gatekeeper quarantine: `xattr -d com.apple.quarantine
  ~/.claude/skills/socialfetch/scripts/socialfetch`.

**"Bridge not running" when fetching a LinkedIn URL.**
- Run `socialfetch bridge status` to confirm.
- Run `socialfetch bridge start` if it's not running.
- Run `socialfetch bridge status` again — if it says *connected* you're
  set; if *not connected*, open Chrome with the extension loaded.

**"X search returns 0 results within the last 7 days."**
- This is the X v2 recent-search tier limit. The CLI pre-flight rejects
  windows older than 7 days with a clear message; if you need older,
  you'll need a paid X API tier.

## Where to ask questions

- File-level conventions: see [CLAUDE.md](CLAUDE.md) at the repo root.
- Feature requests / bugs: open an issue at
  https://github.com/patrickdebois/social-skills/issues.
