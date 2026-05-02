// Package article handles HTML article pages — Medium, Substack, blog
// posts, news sites, and anything else that exposes useful Open Graph or
// schema.org metadata. It's the catch-all fetcher: it claims any http(s)
// URL not already grabbed by a more specific source.
//
// The fetcher itself is a thin shell: download the page, parse it with
// htmlmeta, then dispatch to a host-specific extractor (Medium, Substack)
// or fall back to the generic one. Adding a new host is just a new
// Extractor implementation.
package article

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/htmlmeta"
)

// Extractor turns a parsed HTML page into a populated *core.Item. Each
// Extractor decides which hosts it claims and how aggressively it
// rewrites the article body — Medium-specific extractors strip "More
// from author" sections, the generic one is conservative.
type Extractor interface {
	Name() string
	Match(host string) bool
	Extract(rawURL string, page *htmlmeta.Page) (*core.Item, error)
}

// Fetcher pulls a URL, parses it, and runs it through the first matching
// Extractor. Extractors are tried in registration order; the generic one
// is registered last so per-host ones win.
type Fetcher struct {
	extractors []Extractor
}

func New() *Fetcher {
	return &Fetcher{
		extractors: []Extractor{
			&MediumExtractor{},
			&SubstackExtractor{},
			&GenericExtractor{},
		},
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

	body, err := core.GetBytes(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("article: %w", err)
	}
	page, err := htmlmeta.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("article: parse: %w", err)
	}

	host := hostOf(raw)

	// --generic-extraction forces the catch-all path even on hosts a
	// specific extractor would claim. Useful for debugging when the
	// host-specific output looks wrong, or for sites whose DOM has
	// drifted from what the host extractor expects.
	if opts.GenericExtraction {
		opts.Audit.Logf("article: forced generic extractor (host=%s)", host)
		return (&GenericExtractor{}).Extract(raw, page)
	}

	for _, ex := range f.extractors {
		if ex.Match(host) {
			opts.Audit.Logf("article: %s extractor", ex.Name())
			return ex.Extract(raw, page)
		}
	}
	// Generic claims everything, so reaching here means a misconfiguration.
	return nil, fmt.Errorf("article: no extractor matched host %q", host)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Host, "www.")
}
