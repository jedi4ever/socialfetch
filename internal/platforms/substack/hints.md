# Substack — quirks & gotchas

## Default fetch chain — bridge first for paywall, then http/headless/jina

Substack has a paid-subscriber paywall on member-only posts.
Same shape as Medium: bridge (your logged-in subscriber session)
is the only path to the full body of paywalled posts; anonymous
transports see the public excerpt only.

Default chain lives in
`internal/platforms/substack/fetch.go`'s `defaultChain` var:

1. **`bridge`** — logged-in session opens paid posts
2. **`http`** — direct GET; works for free posts and the public
   excerpt of paid ones (Substack server-renders the excerpt)
3. **`headless`** — local stealth Chromium; useful when a
   newsletter uses a custom domain with light JS rendering
4. **`jina`** — remote service catch-all

Override per call via `SOCIAL_FETCH_CHAIN_SUBSTACK`:

```bash
# always anonymous
SOCIAL_FETCH_CHAIN_SUBSTACK=http,headless,jina

# never hit Jina (network-isolated)
SOCIAL_FETCH_CHAIN_SUBSTACK=bridge,http,headless
```

## Substack works well anonymously

Unlike Medium and LinkedIn, Substack's free-tier posts render
fully into server-side HTML with stable selectors
(`div.body.markup`) that match in BOTH bridge and chromedp DOMs.
That's why the headless transport on Substack returns the same
body length as bridge for public posts — and why we don't ship
a headless-specific extractor for substack (the existing one
works for both).

## URL formats

- Subdomain newsletter: `https://<sub>.substack.com/p/<slug>`
- Custom domain: `https://www.example.com/p/<slug>` — falls
  through to the article fetcher unless wired into the Substack
  fetcher's Match.

## No API key

Substack has no public read API. Paid-tier reading requires the
session cookie of an active subscription, surfaced via the
bridge transport.
