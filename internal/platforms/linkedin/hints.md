# LinkedIn — quirks & gotchas

## Default fetch chain — headless first, bridge for comments

`social-fetch fetch <linkedin-url>` tries the **headless** transport
first (chromedp drives a stealth Chromium and extracts from
LinkedIn's guest-preview page); falls through to the **bridge**
(your logged-in browser session) when headless fails OR when the
caller has set a chain that prefers bridge; Jina is the
remote-service catch-all.

The exact default order lives in the code
(`internal/platforms/linkedin/fetch.go`'s `defaultChain` var) so
this doc doesn't drift — override per call via
`SOCIAL_FETCH_CHAIN_LINKEDIN`:

```bash
# headless first, bridge for comment-thread fallback (current default)
unset SOCIAL_FETCH_CHAIN_LINKEDIN

# bridge first — pick this when you need the comment thread
SOCIAL_FETCH_CHAIN_LINKEDIN=bridge,headless,jina

# always anonymous, never touch the bridge or your real browser
SOCIAL_FETCH_CHAIN_LINKEDIN=headless,jina

# legacy bridge-only behaviour
SOCIAL_FETCH_CHAIN_LINKEDIN=bridge
```

Trade-off matrix (which transport produces what):

| Field    | headless           | bridge          | jina                                     |
|----------|--------------------|-----------------|------------------------------------------|
| body     | full (guest preview) | full           | full (guest preview)                     |
| title    | clean (`og:title`) | "(N) Post \| LinkedIn" (chrome) | empty                       |
| author   | LD+JSON or DOM     | parsed from DOM | parsed from `Name \| LinkedIn` title    |
| comments | always empty       | full thread     | always empty                             |
| media    | hero image         | structured tree | inline `![](url)` only                   |
| auth     | none               | your session    | none                                     |

If you specifically need comments, override the chain to put
bridge first and have the daemon + extension running. The
headless path will never produce them — they're auth-walled.

## Speeding up headless: the daemon

`social-fetch headless start` daemonises a pool of warm Chromium
browsers. With the daemon running, headless fetches drop from
~5-6s (cold spawn) to ~3s (warm tab in an existing browser). See
`social-fetch headless --help` and the top-level HINTS.md
"Headless browser pool" section for tuning.

## URL tracking params get stripped automatically

LinkedIn share URLs come back with `?utm_source=…`,
`?trackingId=…`, `?rcm=…` and similar tracking junk that the
post ID doesn't depend on (the activity ID is in the path). The
fetcher strips `?…` and `#…` before dispatch so `Item.URL` and
the ledger dedup key stay stable across re-shares. Audit log
lines `linkedin: stripped tracking params from URL` mark when
this happened.

## Bridge requires a logged-in browser session

The bridge method routes every LinkedIn request — fetch, search,
timeline — through the local browser-bridge extension, which uses
**your own logged-in browser session** to load pages. Setup:

1. Load `extensions/chrome/` in `chrome://extensions/` (Developer
   mode → Load unpacked).
2. Be signed into LinkedIn in the browser the extension is
   installed in.
3. Run `social-fetch bridge start` (or `bridge run` for foreground).
4. Verify `social-fetch bridge status` returns `connected`.

## **Use sparingly** — LinkedIn aggressively rate-limits scrapers

LinkedIn detects accounts that scrape and **temp-bans them**. If you
search 50 different queries in 10 minutes, you'll likely get a
"this page isn't available" lockout for hours. Mitigation:

- Cache aggressively — the ledger auto-saves every fetch; check
  `social_ledger_seen` before re-fetching.
- Prefer `-p tavily` / `-p serpapi` / `-p perplexity` for general
  "who's writing about X" questions; only reach for `-p linkedin`
  when LinkedIn-specific posts are explicitly the goal.
- Spread out batch operations — wait between calls.
- If you get a lockout, the only fix is to wait (typically
  hours) and avoid LinkedIn from that account for a while.

## Timeline kinds

`-k all` (default), `-k posts`, `-k comments`, `-k reactions`. Each
kind hits a different LinkedIn URL pattern. Reactions are the
shortest dataset and the most rate-limit-friendly.

## URL formats

- Profile: `https://www.linkedin.com/in/<vanity-or-id>/`
- Post: `https://www.linkedin.com/posts/<user>-activity-<id>-<…>` —
  the activity ID is what social-fetch keys on.
- Article: `https://www.linkedin.com/pulse/<slug>` — handled by the
  article fetcher, not the LinkedIn fetcher.

## No API token shape

There's no `LINKEDIN_API_KEY` to set. LinkedIn's official Marketing
API is for ads accounts, not content access. The headless and
Jina paths are fully anonymous (no key needed); the bridge path
uses your own session cookie via the extension.
