# Medium — quirks & gotchas

## Default fetch chain — bridge first for paywall, then http/headless/jina

Medium has a member-only paywall. The bridge transport (your
logged-in Medium session in your real browser) is the only way to
fetch paywalled posts in full — anonymous transports see at most
the public excerpt.

Default chain lives in
`internal/platforms/medium/fetch.go`'s `defaultChain` var:

1. **`bridge`** — logged-in session opens member-only posts
2. **`http`** — direct GET; works for free posts, returns excerpt
   for paywalled ones
3. **`headless`** — local stealth Chromium; better than `http` for
   JS-rendered shell pages, but still anonymous (no member content)
4. **`jina`** — remote service catch-all

Override per call via `SOCIAL_FETCH_CHAIN_MEDIUM`:

```bash
# always anonymous, skip the bridge entirely
SOCIAL_FETCH_CHAIN_MEDIUM=http,headless,jina

# only the local browser pool (no daemon dep on Jina)
SOCIAL_FETCH_CHAIN_MEDIUM=bridge,http,headless

# Jina-only (e.g. air-gapped from your local Chrome)
SOCIAL_FETCH_CHAIN_MEDIUM=jina
```

## Anti-bot is real — anonymous fetches degrade

Medium occasionally serves a "Sign in / loading" shell to repeated
anonymous chromedp visits — the `<article>` element comes back
empty. The headless extractor falls through to og:description as a
last-resort body so the Item still carries SOMETHING, but the
agent should treat short headless results as a hint to retry via
bridge.

The headless daemon's recycle counter
(`SOCIAL_FETCH_HEADLESS_RECYCLE_AFTER`, default 50) caps how many
fetches a single browser handles before it's torn down + respawned
— this rotates fingerprint and slows down anti-bot detection
across batch runs.

## URL formats

- Member post: `https://medium.com/@<user>/<slug>-<hash>`
- Custom domain: `https://blog.example.com/<slug>` — match falls
  through to the article fetcher unless explicitly subdomain-routed.
- Subdomain post: `https://<user>.medium.com/<slug>`

## No API key

Medium has no public read API for the kind of content social-fetch
fetches. Member content is behind authentication; the only way to
read it is your own session via the bridge.
