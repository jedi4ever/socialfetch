# Reddit — quirks & gotchas

## **Per-IP rate limit, no auth toggle**

Reddit's public `.json` endpoint is unauthenticated, which sounds
free, but it rate-limits per source IP — typically ~60 requests per
10 minutes for anonymous traffic. Burst through that and you'll get
`HTTP 429` or empty 200s.

Mitigation:
- Cache aggressively (the ledger does this automatically).
- Spread out batch fetches; add `-j 1` (single-threaded) to avoid
  burst-tripping the limiter.
- If you have a Reddit account, paying tiers via the official API
  raise the cap — not wired up here today.

## URL shapes that work

- Post:    `https://www.reddit.com/r/<sub>/comments/<id>/<slug>/`
- Short:   `https://redd.it/<id>` (we follow the redirect)
- Old UI:  `https://old.reddit.com/r/<sub>/comments/<id>/...` — works,
           treated identically.

The trailing slash and the slug segment are both optional in the
URL we accept — Reddit's API only needs the post ID.

## Search is best-effort

`social-fetch search -p reddit` hits Reddit's `/search.json` which
is the same anonymous endpoint with the same per-IP rate limit. It
also returns *less relevant* results than logged-in search — Reddit
biases anonymous results toward popular subs and downranks niche ones.
For research-grade search across subreddits, `-p tavily` or
`-p serpapi` with a `site:reddit.com` filter often returns better hits.

## Comments can be huge

A popular thread might have 5,000+ comments. `--no-comments` skips
the tree entirely; `--max-comments 50` caps it at the top N (BFS
order). Default fetches everything which can be slow and produce
multi-MB JSON.
