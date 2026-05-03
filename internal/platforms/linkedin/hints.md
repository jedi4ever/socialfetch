# LinkedIn — quirks & gotchas

## Requires the **local browser bridge** + a logged-in session

LinkedIn has no anonymous read path. social-fetch routes every
LinkedIn request — fetch, search, timeline — through the local
browser-bridge extension, which uses **your own logged-in browser
session** to load pages. Without the bridge running and connected,
all LinkedIn calls fail with `bridge unreachable`.

Setup:
1. Load `extensions/chrome/` in `chrome://extensions/` (Developer
   mode → Load unpacked).
2. Be signed into LinkedIn in the browser the extension is installed
   in.
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

## No API token shape — bridge is the auth

There's no `LINKEDIN_API_KEY` to set. LinkedIn's official Marketing
API is for ads accounts, not content access. The bridge IS the auth.
