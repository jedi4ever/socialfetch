# X (Twitter) — quirks & gotchas

## Search is **strictly 7 days only**

`social-fetch search -p x` hits X v2's *recent search* endpoint, which
caps history at the last 7 days. There's no `--last 30d` workaround on
this provider — the API simply rejects `start_time` further back than
7 days with a `HTTP 400`. social-fetch detects this pre-flight and
returns a local error:

```
x search: --after must be within the last 7 days (X v2 recent search
hard cap); for older tweets use full-archive (paid tier) or scrape via
a different provider.
```

Workarounds:
- For older tweets, switch providers: `-p tavily` or `-p serpapi` can
  surface tweet URLs from longer windows via the public web index.
- Full-archive search is X's **paid** tier (`/2/tweets/search/all`,
  Pro+ plan). Not wired up here today.

## Auth — Consumer keys only, NOT bearer / OAuth2

The env vars `X_API_KEY` + `X_API_SECRET` are the **Consumer Keys**
shown at the top of the X Developer Portal under "Keys and tokens" →
**Consumer Keys (API Key and Secret)**.

Do **not** paste:
- The pre-minted **Bearer Token** — social-fetch mints its own at
  runtime via OAuth 2.0 App-Only.
- The **Access Token / Access Token Secret** — those are user-context
  OAuth 1.0a, only needed for *posting* tweets.
- The **OAuth 2.0 Client ID / Client Secret** — that's the newer PKCE
  flow, different shape entirely.

## Timeline = active user's tweets, not their followers' feed

`social-fetch timeline @<handle> -p x` returns *that user's own posts*
(tweets, replies, retweets) — not their home feed. There's no API
access to the home feed for third-party apps.

## Rate limits bite fast on the free tier

X v2's free tier is generous about *which* endpoints you can call but
miserly about *how often*. Recent search caps at ~180 requests / 15
minutes per app. Bulk timeline fetches across many users will trip
the limiter quickly — back off when you see `HTTP 429`.
