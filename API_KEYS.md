# API keys & auth

Every key is **optional**. Features gated on a missing key just degrade gracefully — Tavily search errors with a clear message, YouTube comments are skipped, the X fetcher falls back to the public syndication endpoint, etc.

`social-fetch` reads `.env` files automatically on startup, in this order, **without overriding values already exported in the shell**:

1. `./.env` — the directory you're running from
2. `<binary_dir>/.env` — next to the installed binary (typically `~/.claude/skills/social-fetch/.env`)

Drop a `.env` at either location with whichever keys you need:

```
# X / Twitter
X_API_KEY=...
X_API_SECRET=...

# Search & ask providers
TAVILY_API_KEY=...
SERPAPI_KEY=...
BRAVE_API_KEY=...
PERPLEXITY_API_KEY=...
XAI_API_KEY=...
OPENAI_API_KEY=...
ANTHROPIC_API_KEY=...
GEMINI_API_KEY=...                   # for `google` ask provider (or reuse GOOGLE_API_KEY)
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
HTML2MD_PROVIDER=kaufmann            # kaufmann (default) | builtin (legacy hand-roll)
HTML2MD_READER=local                 # local (default) | jina (route article fetches via r.jina.ai)
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

## SerpAPI — `SERPAPI_KEY`

**Used by:** `serpapi` search provider, `serpapi` ask provider (Google AI Overview engine).

1. Go to **[serpapi.com](https://serpapi.com/)** → sign up.
2. **Dashboard → API Key** → copy.

Free tier: 100 searches/month. The ask path uses the `google_ai_overview` engine which only returns content when Google generates an AI Overview for the query (not every query qualifies).

---

## Perplexity — `PERPLEXITY_API_KEY`

**Used by:** `perplexity` ask provider (Sonar models) **and** `perplexity` search provider (the dedicated `/search` endpoint that returns raw results — title/url/snippet — without LLM synthesis. Same key, cheaper per call since no tokens are billed).

1. Go to **[www.perplexity.ai/settings/api](https://www.perplexity.ai/settings/api)** → sign in.
2. **API Keys → Generate** → copy.

Add a small payment method to enable API access (pay-per-token from Sonar prices). Default model: `sonar` — cheap, fast. Override with `--model sonar-pro` for larger context, `--model sonar-reasoning` for the reasoning variant.

---

## xAI Grok — `XAI_API_KEY`

**Used by:** `grok` ask provider (Live Search-grounded answers).

1. Go to **[console.x.ai](https://console.x.ai/)** → sign in with X.
2. **API Keys** → create one → copy.

Grounding goes through the Agent Tools API on `/v1/responses` with the
`web_search` tool enabled. xAI bills per-token plus a small per-tool
invocation fee. `GROK_API_KEY` is accepted as an alias.

---

## OpenAI — `OPENAI_API_KEY`

**Used by:** `openai` ask provider (Responses API + built-in `web_search`
tool).

1. Go to **[platform.openai.com/api-keys](https://platform.openai.com/api-keys)** → sign in.
2. **+ Create new secret key** → copy.
3. Make sure your account has billing enabled — the Responses API is
   pay-per-token, with an extra per-call fee for hosted tools like
   `web_search`.

Default model: `gpt-5.5` (auto-tracking alias for the latest 5.5
snapshot). Override with `-m gpt-5.5-mini` for cheaper, or any other
GPT-4-tier-or-later model. Web search isn't supported on 3.5-tier
models. Unlike xAI, OpenAI's Responses API requires `model` at the
request level (HTTP 400 if omitted).

---

## Anthropic Claude — `ANTHROPIC_API_KEY`

**Used by:** `anthropic` ask provider (Messages API + built-in
`web_search` server tool).

1. Go to **[console.anthropic.com/settings/keys](https://console.anthropic.com/settings/keys)** → sign in.
2. **+ Create Key** → copy.
3. Make sure your organization admin has **enabled Web Search** in the
   Claude Console (Settings → Privacy). Without it, requests with the
   `web_search_20250305` tool return an error pointing back to that
   setting.

Default model: `claude-sonnet-4-6` (good balance of cost + quality).
Override with `-m claude-opus-4-7` (strongest reasoning) or `-m
claude-haiku-4-5-20251001` (cheapest). Anthropic doesn't expose a
generic "latest" alias — you'll need to bump the family number when
new generations ship.

Pricing: $10 per 1,000 web searches on top of normal token billing.
Each search counts as one use regardless of result count; errored
searches aren't billed.

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
2. **Add App Password** → give it a name (e.g. "social-fetch") → copy the displayed string (format: `xxxx-xxxx-xxxx-xxxx`).
3. Set both:
   ```
   BLUESKY_HANDLE=you.bsky.social        # or your custom domain handle
   BLUESKY_APP_PASSWORD=xxxx-xxxx-xxxx-xxxx
   ```

> **Never use your account password.** App passwords are scoped, revocable from the same settings page, and don't expose your full account.

---

## HTML→Markdown provider — `HTML2MD_PROVIDER`

Not a key, a routing hint. Picks the local converter the article and
per-host extractors use to turn HTML into clean markdown.

| value | behavior |
|---|---|
| `kaufmann` (default) | wraps `github.com/JohannesKaufmann/html-to-markdown/v2` — actively maintained, good edge-case coverage (tables, strikethrough, complex code blocks) |
| `builtin` | the legacy in-tree hand-roll — pure-Go, dependency-light, more aggressive about stripping layout chrome (nav/footer). Useful when you want to avoid the new dep or compare output. |

Unknown values fall back to `kaufmann`.

## HTML→Markdown reader — `HTML2MD_READER`

Service-backed reader that replaces the local fetch+parse+convert
pipeline for the generic article fetcher. When set, the article
fetcher (`social-fetch fetch <any-url>` for non-host-specific URLs)
sends the URL to the chosen service and uses its returned markdown
verbatim.

| value | behavior |
|---|---|
| `local` (default) | use the local fetch + extractor + Converter chain (no service call) |
| `jina` | route fetches through `r.jina.ai/<url>` — sidesteps Cloudflare challenges and JS-rendered SPAs; no key needed for free tier |

Per-host fetchers (medium, substack, hackernews, reddit, github,
twitter, linkedin, youtube, bluesky, arxiv, rss) ignore this — they
still use their own fetch paths, since they depend on parsed DOM /
API responses that a markdown stream can't replace.

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
