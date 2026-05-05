package htmlmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// jinaReaderBase is the public Jina Reader endpoint. The full URL
// shape is `https://r.jina.ai/<target-url>` — no key needed for the
// free tier, no query-string params, no body.
//
// Why this is useful: Jina executes JavaScript, sidesteps Cloudflare
// challenges that block direct curl, and applies its own readability
// extraction — drop-in for sites where our local fetch + extract
// pipeline returns a 403 or junk content.
const jinaReaderBase = "https://r.jina.ai/"

// JinaOptions centralises the request-shaping knobs we send to
// r.jina.ai on every call. The defaults below are tuned for "best
// quality, fresh content, agent-friendly output" — the same values
// every fetcher gets via NewJinaReader().
//
// These are intentionally not env-driven yet: the user asked to
// centralise the values in code first so they're easy to find and
// swap. The next step (when needed) is to read each field from
// JINA_ENGINE / JINA_NO_CACHE / JINA_FORMAT / JINA_TIMEOUT env vars
// in NewJinaReader, with the constants below as fallbacks.
type JinaOptions struct {
	// Engine picks the renderer Jina uses behind the scenes. "browser"
	// runs a real headless Chromium — slower but handles JS-rendered
	// SPAs, paywalled previews, and Cloudflare challenges. "direct"
	// is a plain HTTP fetch (faster, but no JS / no anti-bot).
	// "cf-browser-rendering" routes through Cloudflare's hosted
	// browser. We default to "browser" because every caller of this
	// reader is already a fallback path — quality matters more than
	// the extra second of latency.
	Engine string

	// NoCache=true sends `X-No-Cache: true` so Jina re-fetches the
	// upstream page rather than serving its cached copy. We default
	// on because the typical use case is "the local fetch just
	// returned junk, give me the live state of the page."
	NoCache bool

	// Format selects the response body shape. "json" gives us the
	// structured `data.content` envelope (lets us also see title /
	// description / url separately if we want them later). "markdown"
	// returns the raw markdown body — what the legacy Read() shape
	// expected.
	Format JinaFormat

	// Timeout is the per-request HTTP deadline. Jina's worst case
	// for heavily-JS-rendered pages is 30-45s; 60s of headroom keeps
	// timeouts rare without letting one bad page hang a batch.
	Timeout time.Duration
}

// JinaFormat is the response-body shape Jina returns.
type JinaFormat string

const (
	// JinaFormatJSON requests `Accept: application/json` and parses
	// the `{data:{content}}` envelope. We default to this because
	// having access to title / url / publishedTime alongside the body
	// is strictly more information than a bare markdown stream.
	JinaFormatJSON JinaFormat = "json"
	// JinaFormatMarkdown requests `Accept: text/markdown` and returns
	// the body bytes verbatim.
	JinaFormatMarkdown JinaFormat = "markdown"
)

// DefaultJinaOptions is the single source of truth for "what does
// every Jina call look like." Mutate via NewJinaReaderWithOptions
// when a specific call site needs different behaviour.
var DefaultJinaOptions = JinaOptions{
	Engine:  "browser",
	NoCache: true,
	Format:  JinaFormatJSON,
	Timeout: 60 * time.Second,
}

// JinaReader hits r.jina.ai. Free tier has no key requirement; paid
// tiers exist but we don't authenticate today.
//
// Reuses core.HTTPClient.Transport for audit instrumentation so each
// call lands in the audit log next to the rest of the platform's HTTP
// activity.
type JinaReader struct {
	BaseURL string
	Client  *http.Client
	Options JinaOptions
}

// NewJinaReader builds a reader with DefaultJinaOptions applied —
// the configuration most callers want. Tests / specialised call
// sites that need different behaviour use NewJinaReaderWithOptions.
func NewJinaReader() *JinaReader {
	return NewJinaReaderWithOptions(DefaultJinaOptions)
}

// NewJinaReaderWithOptions lets a specific call site override the
// defaults — e.g. a test that wants Format=Markdown to skip JSON
// parsing, or a future per-platform call that wants Engine=direct
// for speed on a known-cooperative site.
func NewJinaReaderWithOptions(opts JinaOptions) *JinaReader {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultJinaOptions.Timeout
	}
	return &JinaReader{
		BaseURL: jinaReaderBase,
		Client: &http.Client{
			Timeout:   opts.Timeout,
			Transport: core.HTTPClient.Transport,
		},
		Options: opts,
	}
}

// Read fetches the URL through Jina and returns the markdown body.
// Whether the wire response is JSON or markdown is controlled by
// j.Options.Format; the return type stays markdown either way so
// callers don't have to branch on format.
func (j *JinaReader) Read(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.BaseURL+url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", core.UserAgent)
	if j.Options.Engine != "" {
		req.Header.Set("X-Engine", j.Options.Engine)
	}
	if j.Options.NoCache {
		req.Header.Set("X-No-Cache", "true")
	}
	switch j.Options.Format {
	case JinaFormatJSON:
		req.Header.Set("Accept", "application/json")
	default:
		req.Header.Set("Accept", "text/markdown")
	}

	resp, err := j.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("jina reader: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("jina reader: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	if j.Options.Format == JinaFormatJSON {
		var env jinaEnvelope
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			return "", fmt.Errorf("jina reader: decode json: %w", err)
		}
		return env.Data.Content, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("jina reader: read: %w", err)
	}
	return string(body), nil
}

// jinaEnvelope models the slice of Jina's JSON response we use.
// Jina returns a `{code, status, data:{...}}` wrapper; we read the
// markdown body from data.content. The other data fields (title,
// description, url, publishedTime, images, links) stay accessible
// to a future ReadFull() that returns the whole struct.
type jinaEnvelope struct {
	Code   int `json:"code"`
	Status int `json:"status"`
	Data   struct {
		Title         string `json:"title"`
		Description   string `json:"description"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		PublishedTime string `json:"publishedTime"`
	} `json:"data"`
}
