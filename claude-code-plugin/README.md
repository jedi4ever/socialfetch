# social-fetch — Claude Code plugin

A Claude Code plugin that bundles the [`social-fetch`](https://github.com/jedi4ever/social-skills)
skill so Claude Code knows how to fetch URLs / search / ask / timeline
across HN, Reddit, GitHub, X, LinkedIn, YouTube, Bluesky, arXiv,
Medium, Substack, RSS, and generic articles via the `social-fetch` CLI.

This plugin is purely the skill markdown + a manifest. It does **not**
bundle an MCP server (use the [`.mcpb` Desktop Extension](https://github.com/jedi4ever/social-skills/blob/main/INSTALL.md)
for that path) and it does **not** ship the `social-fetch` binary.

## Prerequisite — install both binaries

The plugin calls two binaries: `social-fetch` (always required) and
`social-ledger` (optional, but auto-enabled when present —
gives you a SQLite + FTS5 history of every fetch / research run
the agent does). Install both with one `go install`:

```bash
# from source (Go 1.26+)
go install github.com/jedi4ever/social-skill./cmd/social-fetch@latest
go install github.com/jedi4ever/social-skill./cmd/social-ledger@latest

# or download a release tarball that includes both:
# https://github.com/jedi4ever/social-skills/releases
```

Confirm:

```bash
social-fetch version           # social-fetch 0.9.x
social-ledger            # prints help banner with version
```

Without `social-ledger` on PATH the plugin still works for
fetch / search / ask / timeline / research — the ledger auto-detect
just stays off silently. Drop the binary in later and ledger
queries become available immediately.

## Install the plugin

In Claude Code:

```
/plugin marketplace add jedi4ever/social-skills
/plugin install social-fetch
```

Or, for local development from a clone of this repo:

```bash
claude --plugin-dir ./claude-code-plugin
```

## API keys

Same env vars the standalone CLI reads (`ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, `PERPLEXITY_API_KEY`, …). Set them in your shell
environment or in a nearby `.env` file — `social-fetch` walks parent
directories looking for one. See
[API_KEYS.md](https://github.com/jedi4ever/social-skills/blob/main/API_KEYS.md)
for what each provider unlocks.

## See also

- [`mcpb-extension/`](https://github.com/jedi4ever/social-skills/tree/main/mcpb-extension) — Claude Desktop Extension (one-click install with API-key prompt UI)
- [`skill/social-fetch/`](https://github.com/jedi4ever/social-skills/tree/main/skill/social-fetch) — Standalone skill (Claude Desktop, no plugin wrapper)
- [`social-fetch mcp --ngrok`](https://github.com/jedi4ever/social-skills/blob/main/INSTALL.md#option-b-remote-mcp-via-ngrok) — Remote MCP for claude.ai
