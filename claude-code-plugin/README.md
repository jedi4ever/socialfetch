# socialfetch — Claude Code plugin

A Claude Code plugin that bundles the [`socialfetch`](https://github.com/jedi4ever/socialfetch)
skill so Claude Code knows how to fetch URLs / search / ask / timeline
across HN, Reddit, GitHub, X, LinkedIn, YouTube, Bluesky, arXiv,
Medium, Substack, RSS, and generic articles via the `socialfetch` CLI.

This plugin is purely the skill markdown + a manifest. It does **not**
bundle an MCP server (use the [`.mcpb` Desktop Extension](https://github.com/jedi4ever/socialfetch/blob/main/INSTALL.md)
for that path) and it does **not** ship the `socialfetch` binary.

## Prerequisite — install the `socialfetch` binary

The skill calls a `socialfetch` command. Install it once before
enabling the plugin:

```bash
# from source (Go 1.22+)
go install github.com/jedi4ever/socialfetch/cmd/socialfetch@latest

# or download a release binary from
# https://github.com/jedi4ever/socialfetch/releases
```

Confirm:

```bash
socialfetch version
# socialfetch 0.8.2
```

## Install the plugin

In Claude Code:

```
/plugin marketplace add jedi4ever/socialfetch
/plugin install socialfetch
```

Or, for local development from a clone of this repo:

```bash
claude --plugin-dir ./claude-code-plugin
```

## API keys

Same env vars the standalone CLI reads (`ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, `PERPLEXITY_API_KEY`, …). Set them in your shell
environment or in a nearby `.env` file — `socialfetch` walks parent
directories looking for one. See
[API_KEYS.md](https://github.com/jedi4ever/socialfetch/blob/main/API_KEYS.md)
for what each provider unlocks.

## See also

- [`mcpb-extension/`](https://github.com/jedi4ever/socialfetch/tree/main/mcpb-extension) — Claude Desktop Extension (one-click install with API-key prompt UI)
- [`skill/socialfetch/`](https://github.com/jedi4ever/socialfetch/tree/main/skill/socialfetch) — Standalone skill (Claude Desktop, no plugin wrapper)
- [`socialfetch mcp --ngrok`](https://github.com/jedi4ever/socialfetch/blob/main/INSTALL.md#option-b-remote-mcp-via-ngrok) — Remote MCP for claude.ai
