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
//
// Fetch path: the article fetcher walks a configurable chain of
// methods (default `http,bridge,jina`). PDF URLs and the
// `HTML2MD_READER=jina` opt-in pre-empt the chain entirely — they're
// service-backed paths that bypass the local fetch+parse pipeline.
// Operators override the chain via SOCIAL_FETCH_CHAIN_ARTICLE.
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
	"github.com/jedi4ever/social-skills/internal/fetchchain"
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
//
// BridgeURL is overridable so tests can point the bridge runner at a
// httptest server. Production uses bridge.DefaultEndpoint.
type Fetcher struct {
	extractor Extractor
	BridgeURL string
}

func New() *Fetcher {
	return &Fetcher{
		extractor: &GenericExtractor{},
		BridgeURL: bridge.DefaultEndpoint,
	}
}

// defaultChain is the order the article fetcher tries when no env
// override is set. HTTP first because it's the cheapest path and most
// articles aren't behind any kind of bot challenge; bridge second
// because the user's logged-in browser handles CF challenges, UA
// sniffing, and JS-rendered SPA shells; Jina last as the
// service-backed catch-all (anti-bot infrastructure + clean markdown).
var defaultChain = []fetchchain.Method{
	fetchchain.MethodHTTP,
	fetchchain.MethodBridge,
	fetchchain.MethodJina,
}

// supportedMethods filters the env var so a typo doesn't disable the
// fetcher (see fetchchain.Resolve).
var supportedMethods = map[fetchchain.Method]bool{
	fetchchain.MethodHTTP:   true,
	fetchchain.MethodBridge: true,
	fetchchain.MethodJina:   true,
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
	// PDF_READER). Pre-empts the fetch chain entirely — PDFs aren't
	// HTML, so http / bridge wouldn't know what to do with them.
	// We check the URL extension up front rather than HEAD-probing
	// first — saves a network round trip on the common case (URL
	// ends in .pdf) at the cost of one false negative class (PDFs
	// served from extension-less URLs).
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
	// runs the URL through r.jina.ai for cleaning. Pre-empts the
	// chain — operators who set this want Jina unconditionally, not
	// "Jina if the local methods fail".
	//
	// Deprecated env var (kept for one release). Operators should
	// migrate to SOCIAL_FETCH_CHAIN_ARTICLE=jina,http,bridge — same
	// effect, fits the per-platform chain model. We surface a
	// deprecation line in the audit log every time HTML2MD_READER is
	// honoured so it's noisy enough to migrate.
	if reader := htmlmd.DefaultReader(); reader != nil {
		opts.Audit.Logf("article: HTML2MD_READER=jina is deprecated; use SOCIAL_FETCH_CHAIN_ARTICLE=jina,http,bridge instead (same behaviour, will be removed in a future release)")
		opts.Audit.Logf("article: routing via service-backed reader (HTML2MD_READER)")
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

	chain := fetchchain.Resolve(fetchchain.FromEnv("article"), defaultChain, supportedMethods)
	runners := map[fetchchain.Method]fetchchain.Runner[*core.Item]{
		fetchchain.MethodHTTP: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaHTTP(ctx, raw, opts)
		},
		fetchchain.MethodBridge: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaBridge(ctx, raw, opts)
		},
		fetchchain.MethodJina: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaJina(ctx, raw, opts)
		},
	}
	item, _, err := fetchchain.Run(ctx, "article", raw, opts.Audit, chain, runners)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// fetchViaHTTP is the local-network path. Performs a plain GET, follows
// redirects, parses the resulting HTML, and runs the body through the
// generic extractor. Returns errors for the three "server rejecting
// our client" signals (redirect loop, UA-sniff block, Cloudflare
// challenge) which the chain treats the same way as any other failure
// — try the next method.
func (f *Fetcher) fetchViaHTTP(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	body, finalURL, cfBlocked, uaBlocked, err := directFetch(ctx, raw)
	switch {
	case err != nil && errors.Is(err, core.ErrRedirectLoop):
		return nil, fmt.Errorf("redirect loop: %w", err)
	case uaBlocked:
		return nil, fmt.Errorf("UA-sniff block (4xx + SPA shell)")
	case cfBlocked:
		return nil, fmt.Errorf("cloudflare challenge")
	case err != nil:
		return nil, err
	}

	item, err := f.parseAndExtract(raw, body, opts)
	if err != nil {
		return nil, err
	}
	item.Extra["via"] = "http"
	// If the HTTP redirect chain resolved to a different URL than
	// the user supplied, prefer it as the canonical URL — that's
	// the page actually rendered. We only override when the extractor
	// didn't already pick up a canonical URL via og:url /
	// link[rel=canonical] — those are usually authoritative.
	if finalURL != "" && finalURL != raw && (item.URL == "" || item.URL == raw) {
		item.URL = finalURL
	}
	return item, nil
}

// fetchViaBridge routes the URL through the local browser bridge.
// Real Chromium with the user's session cookies passes the JS
// challenges and UA-sniff blocks that the plain HTTP path can't.
// Same parse + extract pipeline as the HTTP runner — just a
// different bytes-source.
func (f *Fetcher) fetchViaBridge(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	c := &bridge.Client{Endpoint: f.BridgeURL}
	htmlStr, finalURL, _, err := c.GetHTML(ctx, raw, opts.Audit)
	if err != nil {
		return nil, err
	}
	item, err := f.parseAndExtract(raw, []byte(htmlStr), opts)
	if err != nil {
		return nil, err
	}
	item.Extra["via"] = "bridge"
	if finalURL != "" && finalURL != raw && (item.URL == "" || item.URL == raw) {
		item.URL = finalURL
	}
	return item, nil
}

// fetchViaJina is the service-backed catch-all. Routes through
// r.jina.ai which has its own anti-CF + headless-browser
// infrastructure and returns clean markdown. We don't run it through
// htmlmeta because Jina has already stripped the structural HTML —
// no og: tags / JSON-LD / canonical link survives, so the resulting
// Item is body-only with empty Author / Published / Tags / Media.
func (f *Fetcher) fetchViaJina(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	_ = opts // audit goes through ctx via core.WithAudit
	md, err := htmlmd.NewJinaReader().Read(ctx, raw)
	if err != nil {
		return nil, err
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
			"via":           "jina",
		},
	}, nil
}

// parseAndExtract is the shared "bytes → Item" tail used by both the
// http and bridge runners. Centralised so the per-method runner only
// has to worry about getting the bytes; the parse + extract +
// extra-stamp pipeline is identical.
func (f *Fetcher) parseAndExtract(raw string, body []byte, opts core.Options) (*core.Item, error) {
	page, err := htmlmeta.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	host := hostOf(raw)
	if opts.GenericExtraction {
		opts.Audit.Logf("article: forced generic extractor (host=%s)", host)
	} else {
		opts.Audit.Logf("article: %s extractor", f.extractor.Name())
	}
	item, err := f.extractor.Extract(raw, page)
	if err != nil {
		return nil, err
	}
	if item.Extra == nil {
		item.Extra = map[string]any{}
	}
	return item, nil
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
