package htmlmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
// r.jina.ai on every call. Defaults are tuned for "best quality,
// fresh content, agent-friendly output" — every fetcher gets these
// via NewJinaReader() unless the operator overrides per env var:
//
//	SOCIAL_FETCH_JINA_ENGINE      browser (default) | direct | cf-browser-rendering
//	SOCIAL_FETCH_JINA_NO_CACHE    true (default) | false
//	SOCIAL_FETCH_JINA_FORMAT      json (default) | markdown
//	SOCIAL_FETCH_JINA_TIMEOUT     60s (default) — any time.ParseDuration value
//	SOCIAL_FETCH_JINA_MODEL       (default unset, classic heuristic) | readerlm-v2
//
// The `SOCIAL_FETCH_` prefix matches the rest of the binary
// (`SOCIAL_FETCH_CHAIN_<PLATFORM>`, `SOCIAL_FETCH_AUDIT_*`). Unset /
// empty env vars fall through to DefaultJinaOptions, so unsetting
// everything reproduces the in-code defaults exactly.
type JinaOptions struct {
	// APIKey is the Jina paid-tier bearer token. Sent as
	// `Authorization: Bearer <key>` when non-empty. Required for
	// some advanced features (readerlm-v2 model, higher rate
	// limits); the free tier works without one. Read from
	// JINA_API_KEY env (no SOCIAL_FETCH_ prefix — matches the rest
	// of the binary's API-key vars: X_API_KEY, OPENAI_API_KEY etc).
	APIKey string

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

	// Model swaps Jina's HTML→markdown extraction algorithm. Empty
	// (default) uses the classic heuristic readability extractor.
	// "readerlm-v2" routes through Jina's small LLM tuned for
	// HTML→markdown — better fidelity on tables, structured
	// metadata, and ambiguous markup, at higher latency + cost.
	// Sent as `X-Respond-With` when non-empty.
	//
	// Today the only meaningful value is "readerlm-v2"; future Jina
	// model releases drop in here as additional opt-in strings.
	// Operators flip via SOCIAL_FETCH_JINA_MODEL=readerlm-v2 to
	// A/B compare the two extractors on the same URLs.
	Model string
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

// NewJinaReader builds a reader using DefaultJinaOptions overlaid
// with whatever SOCIAL_FETCH_JINA_* env vars are set. The env vars
// are read once per call (cheap — Getenv is a map lookup) so a
// long-running daemon picks up changes by recreating the reader,
// not by restarting.
//
// Tests / specialised call sites that want to bypass env vars
// entirely should use NewJinaReaderWithOptions(opts) with explicit
// options.
func NewJinaReader() *JinaReader {
	return NewJinaReaderWithOptions(JinaOptionsFromEnv())
}

// JinaOptionsFromEnv returns DefaultJinaOptions overlaid with any
// SOCIAL_FETCH_JINA_* env vars the operator has set. Unknown /
// unparseable values fall through to the default with a warning to
// the audit log — fail-soft, since a typo in a Jina knob shouldn't
// turn off the whole fetcher.
func JinaOptionsFromEnv() JinaOptions {
	opts := DefaultJinaOptions

	if v := strings.TrimSpace(os.Getenv("JINA_API_KEY")); v != "" {
		opts.APIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_JINA_ENGINE")); v != "" {
		opts.Engine = v
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_JINA_NO_CACHE")); v != "" {
		switch strings.ToLower(v) {
		case "true", "1", "yes", "on":
			opts.NoCache = true
		case "false", "0", "no", "off":
			opts.NoCache = false
		}
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_JINA_FORMAT")); v != "" {
		switch strings.ToLower(v) {
		case "json":
			opts.Format = JinaFormatJSON
		case "markdown", "md":
			opts.Format = JinaFormatMarkdown
		}
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_JINA_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			opts.Timeout = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_JINA_MODEL")); v != "" {
		opts.Model = v
	}
	return opts
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
	if j.Options.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+j.Options.APIKey)
	}
	if j.Options.Engine != "" {
		req.Header.Set("X-Engine", j.Options.Engine)
	}
	if j.Options.NoCache {
		req.Header.Set("X-No-Cache", "true")
	}
	if j.Options.Model != "" {
		req.Header.Set("X-Respond-With", j.Options.Model)
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
