# API keys & auth

Every key is **optional**. Features gated on a missing key just degrade gracefully — Tavily search errors with a clear message, YouTube comments are skipped, the X fetcher falls back to the public syndication endpoint, etc.

`socialfetch` reads `.env` files automatically on startup, in this order, **without overriding values already exported in the shell**:

1. `./.env` — the directory you're running from
2. `<binary_dir>/.env` — next to the installed binary (typically `~/.claude/skills/socialfetch/.env`)

Drop a `.env` at either location with whichever keys you need:

```
# X / Twitter
X_API_KEY=...
X_API_SECRET=...

# Search & ask providers
TAVILY_API_KEY=...
BING_API_KEY=...
SERPAPI_KEY=...
BRAVE_API_KEY=...
PERPLEXITY_API_KEY=...
XAI_API_KEY=...
GOOGLE_API_KEY=...
GOOGLE_CSE_ID=...

# Source-specific
YOUTUBE_API_KEY=...                  # same as GOOGLE_API_KEY works too
GITHUB_TOKEN=...                     # raises rate limit
BLUESKY_HANDLE=you.bsky.social
BLUESKY_APP_PASSWORD=xxxx-xxxx-xxxx-xxxx

# Optional knobs
TAVILY_TOPIC=news                    # switch Tavily to news topic for stricter recency
YOUTUBE_TRANSCRIPT_PROVIDER=auto     # auto | ytdlp | innertube | kkdai
```

---

## X / Twitter — `X_API_KEY` + `X_API_SECRET`

**Used by:** `twitter` fetch source (long-form note tweets + replies via `/2/tweets/search/recent`), `x` search provider.

1. Go to **[developer.x.com](https://developer.x.com/)** → sign in with your X account.
2. **Projects & Apps → Create app** → fill out the form. Free tier is fine.
3. Once the app exists: **Keys and tokens → API Key and Secret → Regenerate** → copy both.
4. Free tier covers `tweets/search/recent` (last-7-day window).

> ⚠️ The `bearer token` X also gives you is **not** what we use; we exchange Key+Secret for one programmatically.

---

## Tavily — `TAVILY_API_KEY`

**Used by:** `tavily` search and ask providers.

1. Go to **[tavily.com](https://tavily.com/)** → sign up.
2. Dashboard → **API Keys** → copy.

Free tier: 1,000 searches/month. Optional `TAVILY_TOPIC=news` env var switches to the news index for strict recency filtering at the cost of recall on personal-name / evergreen queries.

---

## Brave Search — `BRAVE_API_KEY`

**Used by:** `brave` search provider. Privacy-focused, doesn't piggyback on Bing/Google rankings, has native `--last 7d` via the `freshness` parameter.

1. Go to **[api.search.brave.com](https://api.search.brave.com/)** → sign up.
2. **Subscriptions** → pick the **Free** plan (2,000 queries/month, 1 q/sec).
3. **API Keys** → copy.

---

## Bing Web Search v7 — `BING_API_KEY`

**Used by:** `bing` search provider.

1. Go to **[portal.azure.com](https://portal.azure.com/)** → Cognitive Services.
2. Create a **"Bing Search v7"** resource.
3. **Keys and Endpoint** → copy Key 1.

> Note: Microsoft has been retiring/migrating Bing Search v7. As of mid-2026 the resource is still creatable but availability varies by Azure region.

---

## SerpAPI — `SERPAPI_KEY`

**Used by:** `serpapi` search provider, `serpapi` ask provider (Google AI Overview engine).

1. Go to **[serpapi.com](https://serpapi.com/)** → sign up.
2. **Dashboard → API Key** → copy.

Free tier: 100 searches/month. The ask path uses the `google_ai_overview` engine which only returns content when Google generates an AI Overview for the query (not every query qualifies).

---

## Perplexity — `PERPLEXITY_API_KEY`

**Used by:** `perplexity` ask provider (Sonar models).

1. Go to **[www.perplexity.ai/settings/api](https://www.perplexity.ai/settings/api)** → sign in.
2. **API Keys → Generate** → copy.

Add a small payment method to enable API access (pay-per-token from Sonar prices). Default model: `sonar` — cheap, fast. Override with `--model sonar-pro` for larger context, `--model sonar-reasoning` for the reasoning variant.

---

## xAI Grok — `XAI_API_KEY`

**Used by:** `grok` ask provider (Live Search-grounded answers).

1. Go to **[console.x.ai](https://console.x.ai/)** → sign in with X.
2. **API Keys** → create one → copy.

Live Search is enabled per-request by socialfetch (`search_parameters.mode: "on"`); it costs a small per-source fee on top of token usage.

---

## Google — `GOOGLE_API_KEY` (+ `GOOGLE_CSE_ID` for search)

One key powers **three** providers:

- **`youtube` fetch + search** — YouTube Data API v3 (also accepts `YOUTUBE_API_KEY` if you want to keep them separate).
- **`google` ask** — Gemini API with the built-in `google_search` tool (also accepts `GEMINI_API_KEY`).
- **`google` search** — Custom Search JSON API. Requires an additional **engine ID**.

### Step 1 — get a key

1. Go to **[console.cloud.google.com](https://console.cloud.google.com/)** → New Project (or existing).
2. **APIs & Services → Library** → enable any of:
   - **YouTube Data API v3**
   - **Custom Search API** (for `google` search)
   - **Generative Language API** (for `google` ask via Gemini)
3. **APIs & Services → Credentials → + Create Credentials → API key** → copy.
4. Optional: click **Edit** → restrict the key to only the APIs above.

### Step 2 — Custom Search Engine ID (only if using `google` search)

1. Go to **[programmablesearchengine.google.com](https://programmablesearchengine.google.com/)** → **Add**.
2. Configure to **"Search the entire web"**.
3. Copy the **Search engine ID** (looks like `xx0xxx00x0xxxxx0x`).

### Free quotas

| API | Free tier |
|---|---|
| YouTube Data API v3 | 10,000 units/day. Metadata + comments calls = 1 unit each; search = 100 units. |
| Custom Search JSON | 100 q/day (then $5 per 1k). |
| Gemini (Generative Language) | 1,500 requests/day on `gemini-2.5-flash` free tier, generous for casual use. |

---

## Bluesky — `BLUESKY_HANDLE` + `BLUESKY_APP_PASSWORD`

**Used by:** `bluesky` search provider (the post fetcher is unauthenticated and works without these).

Bluesky's `app.bsky.feed.searchPosts` requires an authenticated session. We do the `com.atproto.server.createSession` call once on startup and cache the JWT.

1. Go to **[bsky.app/settings/app-passwords](https://bsky.app/settings/app-passwords)**.
2. **Add App Password** → give it a name (e.g. "socialfetch") → copy the displayed string (format: `xxxx-xxxx-xxxx-xxxx`).
3. Set both:
   ```
   BLUESKY_HANDLE=you.bsky.social        # or your custom domain handle
   BLUESKY_APP_PASSWORD=xxxx-xxxx-xxxx-xxxx
   ```

> **Never use your account password.** App passwords are scoped, revocable from the same settings page, and don't expose your full account.

---

## YouTube transcript provider switch — `YOUTUBE_TRANSCRIPT_PROVIDER`

Not a key, a routing hint. See README for the full provider table; valid values:

| value | behavior |
|---|---|
| `auto` (default) | yt-dlp if installed → InnerTube → kkdai. First success wins. |
| `ytdlp` | shells out to `yt-dlp` (most reliable; install with `brew install yt-dlp` or `pip install yt-dlp`) |
| `innertube` | pure-Go scrape via `youtubei/v1/get_transcript` — fragile but no extra dep |
| `kkdai` | `kkdai/youtube/v2`'s caption-track endpoint |

---

## GitHub — `GITHUB_TOKEN`

**Used by:** `github` fetch source. Optional.

Without a token: **60 requests/hour** per IP.
With a token: **5,000 requests/hour**.

1. Go to **[github.com/settings/tokens](https://github.com/settings/tokens)**.
2. **Generate new token** (classic or fine-grained). For public repos no scopes are needed.
3. Copy.
