// Package article handles generic HTML article pages — blog posts,
// news sites, and anything else that exposes useful Open Graph or
// schema.org metadata. It's the catch-all fetcher: it claims any http(s)
// URL not already grabbed by a more specific source.
//
// Per-host extractors (Medium, Substack) live in their own platform
// packages alongside their bridge-aware fetchers. Those packages reuse
// this package's BaseFromPage / RenderArticle helpers but own their
// site-specific selectors. The article package itself only ships the
// GenericExtractor.
package article

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/bridge"
	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/render/htmlmd"
	"github.com/jedi4ever/social-skills/internal/util/htmlmeta"
)

// Extractor turns a parsed HTML page into a populated *core.Item. The
// interface is exported so platform packages with their own extractors
// (medium, substack) implement the same contract — useful when other
// code wants to handle a heterogeneous list of extractors uniformly.
type Extractor interface {
	Name() string
	Match(host string) bool
	Extract(rawURL string, page *htmlmeta.Page) (*core.Item, error)
}

// Fetcher pulls a URL, parses it, and runs it through GenericExtractor.
// Per-host fetchers (medium, substack) are registered before this in
// the top-level fetch registry so they claim their hosts first.
type Fetcher struct {
	extractor Extractor
}

func New() *Fetcher {
	return &Fetcher{
		extractor: &GenericExtractor{},
	}
}

func (Fetcher) Name() string { return "article" }

// Match accepts any http(s) URL. Because the registry consults fetchers
// in order, this should be registered LAST in the top-level fetch
// registry so more specific fetchers (hackernews, reddit, github,
// twitter, rss) get first dibs.
func (Fetcher) Match(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https")
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	ctx = core.WithAudit(ctx, opts.Audit)

	// PDF early-exit. Local extractors don't read PDFs at all (they
	// expect HTML), so anything that looks like a PDF gets routed
	// through PDFReader (Jina by default, configurable via
	// PDF_READER). We check the URL extension up front rather than
	// HEAD-probing first — saves a network round trip on the common
	// case (URL ends in .pdf) at the cost of one false negative class
	// (PDFs served from extension-less URLs, which we'd discover
	// through the failing HTML parse below; a future improvement is
	// to retry via PDFReader on parse failure when the response's
	// Content-Type is application/pdf).
	if htmlmd.IsPDFURL(raw) {
		reader := htmlmd.DefaultPDFReader()
		if reader == nil {
			return nil, fmt.Errorf("article: %s looks like a PDF but PDF_READER is disabled (set PDF_READER=jina or unset to enable the default Jina-based reader)", raw)
		}
		opts.Audit.Logf("article: PDF detected, routing via %T", reader)
		md, err := reader.Read(ctx, raw)
		if err != nil {
			return nil, fmt.Errorf("article: PDF read: %w", err)
		}
		return &core.Item{
			Source:      "article",
			Kind:        "pdf",
			URL:         raw,
			CanonicalID: raw,
			Content:     strings.TrimSpace(md),
			FetchedAt:   time.Now().UTC(),
			Extra: map[string]any{
				"requested_url": raw,
				"via":           "pdf-reader",
			},
		}, nil
	}

	// HTML2MD_READER=jina opts into a service-backed fetch path that
	// runs the URL through r.jina.ai for cleaning. Useful when the
	// site is behind Cloudflare or renders only via JS — Jina handles
	// both. Skips the local GetBytes + htmlmeta parse + extractor
	// chain entirely; we still build a metadata-bearing core.Item but
	// the body comes pre-cleaned as markdown.
	if reader := htmlmd.DefaultReader(); reader != nil {
		opts.Audit.Logf("article: routing via service-backed reader")
		md, err := reader.Read(ctx, raw)
		if err != nil {
			return nil, fmt.Errorf("article: %w", err)
		}
		return &core.Item{
			Source:      "article",
			Kind:        "article",
			URL:         raw,
			CanonicalID: raw,
			Content:     strings.TrimSpace(md),
			FetchedAt:   time.Now().UTC(),
			Extra: map[string]any{
				"requested_url": raw,
				"via":           "reader",
			},
		}, nil
	}

	// Direct fetch first. We do the GET ourselves (rather than
	// core.GetBytes) so we can inspect headers + status before
	// committing to the response body — needed for CF detection.
	body, finalURL, cfBlocked, uaBlocked, err := directFetch(ctx, raw)
	via := "http"
	// Three "the server is rejecting our client" signals all flow to
	// the same Jina-Reader fallback because Jina has browser-like
	// infrastructure that handles each:
	//
	//   - redirect loops (server 302s to itself forever; milvus.io,
	//     some nginx region-detection setups)
	//   - UA-sniff 4xx (server returns 404 + SPA shell when our UA
	//     isn't a real browser)
	//   - Cloudflare challenges (handled below; falls through to
	//     bridge first because logged-in browser sessions matter)
	if err != nil && errors.Is(err, core.ErrRedirectLoop) {
		opts.Audit.Logf("article: redirect loop detected, trying Jina Reader")
		return jinaFallback(ctx, raw, "jina-redirect-loop-fallback")
	}
	if uaBlocked {
		opts.Audit.Logf("article: UA-sniff block detected (4xx + SPA shell), trying Jina Reader")
		return jinaFallback(ctx, raw, "jina-ua-block-fallback")
	}
	if err != nil && !cfBlocked {
		return nil, fmt.Errorf("article: %w", err)
	}
	if cfBlocked {
		// CF detection happens after the redirect chain settles, so
		// finalURL still records the post-redirect target — useful
		// even when we fall through to bridge/Jina.
		// Try the browser bridge. Real Chromium with the user's
		// session cookies passes the JS challenge that our HTTP
		// client cannot.
		opts.Audit.Logf("article: cloudflare challenge detected, trying bridge")
		c := &bridge.Client{Endpoint: bridge.DefaultEndpoint}
		htmlStr, _, _, berr := c.GetHTML(ctx, raw, opts.Audit)
		switch {
		case berr == nil:
			body = []byte(htmlStr)
			via = "bridge"
		case errors.Is(berr, bridge.ErrBridgeUnreachable) || errors.Is(berr, bridge.ErrNoExtensionAttached):
			// Bridge isn't running. Last-resort fallback: route the
			// URL through Jina Reader, which has its own anti-CF
			// infrastructure and returns markdown directly. We bypass
			// the htmlmeta+extractor chain since Jina's output is
			// already clean.
			opts.Audit.Logf("article: bridge unavailable (%v), trying Jina Reader", berr)
			md, jerr := htmlmd.NewJinaReader().Read(ctx, raw)
			if jerr != nil {
				return nil, fmt.Errorf("article: cloudflare blocked + bridge unavailable + jina failed: %w", jerr)
			}
			return &core.Item{
				Source:      "article",
				Kind:        "article",
				URL:         raw,
				CanonicalID: raw,
				Content:     strings.TrimSpace(md),
				FetchedAt:   time.Now().UTC(),
				Extra: map[string]any{
					"requested_url": raw,
					"via":           "jina-cf-fallback",
				},
			}, nil
		default:
			// Bridge gave a real error (timeout, navigation failure).
			// Surface it.
			return nil, fmt.Errorf("article: cloudflare blocked, bridge fetch failed: %w", berr)
		}
	}

	page, err := htmlmeta.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("article: parse: %w", err)
	}

	host := hostOf(raw)

	// --generic-extraction is now a no-op for this fetcher (the only
	// extractor here IS the generic one) — kept as a logged signal so
	// the audit trail still records the user's intent. Per-host
	// extractors live in their own packages and have their own bypass.
	if opts.GenericExtraction {
		opts.Audit.Logf("article: forced generic extractor (host=%s)", host)
	} else {
		opts.Audit.Logf("article: %s extractor (via=%s)", f.extractor.Name(), via)
	}
	item, err := f.extractor.Extract(raw, page)
	if err != nil {
		return nil, err
	}
	if item.Extra == nil {
		item.Extra = map[string]any{}
	}
	item.Extra["via"] = via
	// If the HTTP redirect chain resolved to a different URL than
	// the user supplied, prefer it as the canonical URL — that's
	// the page actually rendered. Registry.Fetch will then stamp
	// item.RequestURL with the original `raw` automatically (since
	// raw != item.URL), so consumers downstream (the ledger, the
	// JSONL output) see both.
	//
	// We only override when the extractor didn't already pick up a
	// canonical URL via og:url / link[rel=canonical] — those are
	// usually authoritative and the redirect target may be a
	// CDN-prefixed or query-parameterised variant the publisher
	// intentionally redirected away from.
	if finalURL != "" && finalURL != raw && (item.URL == "" || item.URL == raw) {
		item.URL = finalURL
	}
	return item, nil
}

// jinaFallback routes a URL through r.jina.ai and packages the
// returned markdown as a core.Item. Centralized here because three
// different upstream failure modes (redirect loops, UA-sniff blocks,
// Cloudflare challenges with bridge unavailable) all flow to the
// same recovery path. The `via` arg is stamped into Extra so audit
// readers / agents can tell which failure triggered the fallback.
func jinaFallback(ctx context.Context, raw, via string) (*core.Item, error) {
	md, err := htmlmd.NewJinaReader().Read(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("article: jina fallback failed (%s): %w", via, err)
	}
	return &core.Item{
		Source:      "article",
		Kind:        "article",
		URL:         raw,
		CanonicalID: raw,
		Content:     strings.TrimSpace(md),
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"requested_url": raw,
			"via":           via,
		},
	}, nil
}

// directFetch performs a plain HTTP GET and inspects the response for
// Cloudflare challenges before returning. Four return cases:
//
//   - (body, false, false, nil) — 2xx success, body is the page bytes
//   - (nil,  true,  false, nil) — CF challenge detected, retry via bridge/Jina
//   - (nil,  false, true,  nil) — UA-sniff block (4xx with substantial HTML
//     body, e.g. milvus.io which serves 404
//     Next.js shells to non-browser UAs).
//     Caller should retry via Jina.
//   - (nil,  false, false, err) — real network or HTTP-level error, no recovery
//
// directFetch GETs raw and returns the body + the post-redirect URL.
// resp.Request.URL is what net/http mutates as it follows the redirect
// chain, so by the time Do() returns it points at the final landing
// page. Equal to `raw` when there was no redirect.
//
// We read the response body even on non-2xx so we can fingerprint
// the challenge HTML — the headers alone aren't always enough.
// Capped at 4 KiB for the snippet to bound memory; the full body is
// returned only on 2xx.
func directFetch(ctx context.Context, raw string) (body []byte, finalURL string, cfBlocked, uaBlocked bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", false, false, err
	}
	req.Header.Set("User-Agent", core.UserAgent)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, "", false, false, err
	}
	defer resp.Body.Close()
	finalURL = raw
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		b, rerr := io.ReadAll(resp.Body)
		return b, finalURL, false, false, rerr
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if core.IsCloudflareBlocked(resp, snippet) {
		return nil, finalURL, true, false, nil
	}
	// 4xx with a substantial HTML body that smells like an SPA shell
	// (Next.js / React / Vue scaffolding) is almost always UA-
	// sniffing — the server has the content but won't serve it to
	// our client. milvus.io is the canonical example: returns 404 +
	// a 2.5 KB Next.js shell when the cookie challenge fails. Real
	// 404s are tiny ("Not Found" / a sentence) and don't carry
	// `_next/`-style asset references. Trigger the UA-blocked path
	// when both signals match.
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
		strings.Contains(ct, "text/html") && looksLikeSPAShell(snippet) {
		return nil, finalURL, false, true, nil
	}
	return nil, finalURL, false, false, fmt.Errorf("GET %s: HTTP %d: %s", raw, resp.StatusCode, core.HTTPErrorBody(resp))
}

// looksLikeSPAShell fingerprints a response body that's clearly a
// JavaScript-app scaffold rather than a real "page not found".
// Conservative — we'd rather miss an edge case than misclassify a
// legitimate 404 with stylesheet links. Triggers on Next.js / Nuxt /
// Vite / Vercel-flavoured markers.
func looksLikeSPAShell(b []byte) bool {
	if len(b) < 1024 {
		return false
	}
	s := strings.ToLower(string(b))
	markers := []string{
		"/_next/",          // Next.js
		"_nuxt/",           // Nuxt
		"data-n-g=",        // Next.js global asset link
		"data-n-p=",        // Next.js page asset link
		"window.__nuxt__",  // Nuxt hydration block
		"id=\"__next\"",    // Next.js root div
		"id=\"app\"></div", // generic SPA root
	}
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Host, "www.")
}
