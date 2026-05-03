# social-fetch — repo conventions

## Error handling for external APIs

**Never swallow HTTP error bodies.** Every external HTTP call in this repo
(search providers, source fetchers, bridge clients) must surface the
underlying API's error message, not just the status code. A bare
`HTTP 400` is a bug — the body almost always contains the real reason
(rate limit hit, invalid parameter, expired key, tier exceeded).

**Use `core.HTTPErrorBody(resp)`** at every non-2xx branch:

```go
if resp.StatusCode < 200 || resp.StatusCode >= 300 {
    return nil, fmt.Errorf("foo: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
}
```

The helper reads up to 512 bytes, collapses whitespace to one line, and
truncates to 256 chars. It's safe to call on any `*http.Response` and
consumes the body (do not read again afterwards).

When an API has a structured error envelope worth parsing (X v2 returns
`{errors:[{message}]}` or `{title,detail}`), add a provider-local
decoder — see `xsearch.decodeXError` for the pattern. Generic providers
should just use `core.HTTPErrorBody`.

## Pre-flight known API limits

When a provider has a hard tier limit that produces a non-actionable
error from the API, validate the request *before* sending and return a
clear local error explaining the constraint. Example: X v2 recent search
caps `start_time` to 7 days — `xsearch.go` rejects out-of-window
`opts.After` with a message that names the constraint, instead of
letting X return a bare `HTTP 400`.

## Selectors that scrape third-party DOMs will drift

LinkedIn, Reddit, X-syndication, etc. rename CSS classes periodically.
When you add a substring matcher, also add a unit test with HTML that
mirrors the current DOM shape — that way the next drift surfaces at
`go test` time instead of as silent empty fields. See
`internal/platforms/linkedin/fetch_extract_comments.go` +
`fetch_test.go` for the pattern (real-DOM HTML fragment fed through
`extractComments`).

## Platform layout

Every platform lives in `internal/platforms/<name>/` and ships one file
per capability:

```
fetch.go          # core.Fetcher impl
fetch_extract.go  # parsing helpers (HTML/JSON → core.Item)
search.go         # core.SearchProvider impl
ask.go            # core.Asker impl
timeline.go       # core.TimelineProvider impl
profile.go        # reserved for future capability
```

Capability interfaces live in `internal/core/{fetcher,search,ask,timeline}.go`.
Adding a platform means adding the package + registering it in
`cmd/social-fetch/main.go`'s `buildRegistries()` / `buildAskers()`. The
help text and `social-fetch list` are derived from the registries.

## Test coverage requirements per platform

**Every capability function on every platform must have BOTH:**

1. **A unit test** — fast, offline, uses an httptest server or canned
   HTML/JSON fixtures. Runs by default with `go test ./...`. Locks in
   parser behaviour and protects against silent regressions when
   third-party DOMs / API shapes drift. File convention:
   `<capability>_test.go`.

2. **A live test** — hits the real upstream, gated behind the `live`
   build tag so it doesn't run on `go test ./...` by default. Use
   `go test -tags=live ./...` to run them. Catches the cases canned
   fixtures miss: auth flows working end-to-end, real DOM shape still
   matches, rate limiting behaviour. File convention: `live_test.go`,
   first line `//go:build live`.

If you can't write one of the two for a capability, document why in the
package doc comment.

## Documentation: every file gets a human-readable opener

Each `.go` file starts with a doc comment that a human can read and
walk away with the gist of the file. For package files, that's the
`// Package <name>` block at the top of the canonical file (often the
fetch.go or platform.go). For each non-trivial type, function, and
exported constant, add a short comment explaining *why* the thing
exists, not just what it does — what problem it solves, what surprising
behaviour it has, what other code it interacts with.

Reading the file with comments only should leave you with a working
mental model. Reading the code without comments should leave you
guessing. Aim for the former.

## Keep `SKILL.md` and `INSTALL.md` in lockstep with the binary

Whenever you add or change user-visible functionality — new subcommand,
new provider, new flag, new env var, removed flag — update the matching
sections in:

- **`skills/social-fetch/SKILL.md`** (provider lists, flag tables, examples,
  decision rules, the `allowed-tools` frontmatter when a new
  subcommand is added). Claude Desktop / Claude Code load this file
  verbatim — stale entries here mean the agent recommends commands the
  binary no longer accepts, or misses ones it now does.
- **`INSTALL.md`** + **`API_KEYS.md`** (auth/env-var docs, install
  steps, free-tier notes). New providers without a matching API_KEYS
  section leave users guessing where to get the key.

Same rule for the in-binary help text in `cmd/social-fetch/main.go`
(`printAskHelp`, `printSearchHelp`, etc.) — `social-fetch help` is the
authoritative reference, so a feature with no help text is invisible.

**Add `extensions/claude-desktop/manifest.json` to that list whenever a new
provider/env-var lands.** The Claude Desktop Extension installer
shows users every entry in `user_config` as a form field; if a new
key (e.g. `NEW_API_KEY`) doesn't get an entry there, users won't
know to set it during install. Keep `user_config` parallel with
`API_KEYS.md` — both should list the same env vars.

**The Claude Code plugin's SKILL.md is generated** from
`skills/social-fetch/SKILL.md` via `make plugin-build` — it's the same
content with `scripts/social-fetch` rewritten to bare `social-fetch`
(plugin assumes PATH install). After editing the standalone SKILL.md,
run `make plugin-build` and commit the regenerated
`extensions/claude-code/skills/social-fetch/SKILL.md` so the marketplace
install (`/plugin marketplace add jedi4ever/social-skills`) picks up
the change. Bump the version in
`extensions/claude-code/.claude-plugin/plugin.json` and
`.claude-plugin/marketplace.json` alongside
`cmd/social-fetch/main.go`'s `Version` and
`extensions/claude-desktop/manifest.json` — all four version fields move
together on every user-visible release.

**The Chrome browser-bridge extension has its own version** in
`extensions/chrome/manifest.json` — independent of `cmd/social-fetch/main.go`'s
`Version`. Bump the Chrome extension's version whenever you change
`extensions/chrome/*.js` (content scripts, background.js, popup, etc.).
`make bridge-package` reads that version field to name the dist
zip; an unbumped version means two zips with the same name and
older Chrome reloads of the same nominal version may not pick up
your changes.

## Versioning

`cmd/social-fetch/main.go` declares a `Version` constant that's
surfaced via `social-fetch version` and the top of `social-fetch help`.
**Bump it on every user-visible release** — new subcommand, new
provider, new flag, removed flag, behaviour change a downstream user
would notice. Bug fixes that don't change behaviour can ride along on
the next feature bump.

We follow loose semver: `MAJOR.MINOR.PATCH`. MAJOR for breaking CLI
changes (removed flags, renamed subcommands), MINOR for additive
changes (new provider, new flag), PATCH for bug fixes only. The
constant lives at the top of `cmd/social-fetch/main.go` next to the
imports — easy to find when you're already editing the file for
something else.
