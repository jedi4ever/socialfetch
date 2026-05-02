# socialfetch — Claude Desktop Extension

This directory holds the `.mcpb` manifest and packaging for socialfetch's
Claude Desktop Extension. The compiled `.mcpb` archive (built by
`make extension-package`) lands in `dist/socialfetch-extension-<version>-<os>-<arch>.mcpb`.

## Install

1. Run `make extension-package` from the repo root. Produces
   `dist/socialfetch-extension-0.2.0-darwin-arm64.mcpb` (macOS Apple
   Silicon only at the moment — see Cross-platform below).

2. Drag the `.mcpb` file onto Claude Desktop's **Settings → Extensions**
   panel, or double-click it. Claude Desktop opens an install dialog.

3. Fill in the API keys you want enabled. Every field is optional —
   leave blank to skip that provider. Sensitive fields (the keys) go
   straight to the macOS Keychain. Non-sensitive routing hints
   (`HTML2MD_PROVIDER`, `HTML2MD_READER`, `TAVILY_TOPIC`) live in
   plain Claude Desktop config.

4. Click **Install**. Claude Desktop will register the bundled MCP
   server and surface six tools to chat: `fetch`, `search`, `ask`,
   `timeline`, `list_providers`, `bridge_status`.

5. Verify by asking Claude Desktop something like *"use socialfetch
   to fetch https://news.ycombinator.com/item?id=1"*. Claude should
   call the `fetch` tool and return the parsed thread.

## Provider chains

The MCP `search` and `ask` tools accept a `provider` argument. Use
`auto` (default) to walk the built-in chain, or `name1,name2,name3`
to define a custom one. Each provider in the chain falls through on
missing keys, errors, or empty results — so a partial key set still
gives useful output.

- Default `ask` chain: `perplexity → grok → openai → anthropic →
  google → tavily → serpapi`.
- Default `search` chain: `perplexity → tavily → brave → serpapi →
  duckduckgo`.

## Browser bridge for LinkedIn / Medium / Substack

The `fetch` tool's LinkedIn / Medium / Substack paths require the
local browser bridge. The extension does NOT manage the bridge for
you — start it from a shell:

```
~/.claude/extensions/socialfetch/scripts/socialfetch bridge start
```

Use the `bridge_status` MCP tool to confirm Claude Desktop can talk
to it.

## Cross-platform

Phase 1 ships **darwin/arm64 only** (the developer's platform). Phase
2 will add `darwin-amd64`, `linux-amd64`, `windows-amd64` builds via
the `extension-package-all` Makefile target — Go cross-compilation is
trivial; the current scope just kept the iteration tight.

## Audit log

The extension's MCP server writes to the same global audit log as
the CLI: `~/Library/Caches/socialfetch/audit.jsonl`. Tail it with
`socialfetch monitor` to watch tool invocations live.

## Validate before shipping

Anthropic ships a validation CLI. Install via npm and check the
manifest before publishing:

```
npm install -g @anthropic-ai/mcpb
mcpb validate dist/socialfetch-extension-*.mcpb
```
