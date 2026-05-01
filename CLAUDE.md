# social-skills — repo conventions

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
`internal/sources/linkedin/comments.go` + `linkedin_test.go` for the
pattern (real-DOM HTML fragment fed through `extractComments`).
