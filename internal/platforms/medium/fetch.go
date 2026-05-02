// Package medium fetches Medium articles by routing through the local
// browser bridge when available — that lets the user's logged-in
// session bypass member-only paywalls — and falling back to a direct
// HTTP fetch when the bridge isn't running.
//
// In both cases we hand the resulting HTML to article.MediumExtractor,
// so the rendered output is identical regardless of which path was used.
package medium

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/htmlmeta"
	"github.com/patrickdebois/social-skills/internal/platforms/article"
)

type Fetcher struct {
	BridgeURL string
	Extractor *article.MediumExtractor
}

func New() *Fetcher {
	return &Fetcher{
		BridgeURL: bridge.DefaultEndpoint,
		Extractor: &article.MediumExtractor{},
	}
}

func (Fetcher) Name() string { return "medium" }

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	return host == "medium.com" || strings.HasSuffix(host, ".medium.com")
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	htmlStr, finalURL, via, err := bridgeOrDirect(ctx, raw, f.BridgeURL, opts.Audit)
	if err != nil {
		return nil, fmt.Errorf("medium: %w", err)
	}

	page, err := htmlmeta.Parse(bytes.NewReader([]byte(htmlStr)))
	if err != nil {
		return nil, fmt.Errorf("medium: parse: %w", err)
	}

	target := finalURL
	if target == "" {
		target = raw
	}
	item, err := f.Extractor.Extract(target, page)
	if err != nil {
		return nil, err
	}
	if item.Extra == nil {
		item.Extra = map[string]any{}
	}
	item.Extra["via"] = via
	return item, nil
}

// bridgeOrDirect tries the browser bridge first. On unreachable bridge
// or no extension attached, it falls back to a direct HTTP GET so the
// fetcher still returns the public/excerpt content. Returns the HTML,
// the final URL (after any browser-resolved redirects), and a marker
// the renderer can record under Extra.via for traceability.
func bridgeOrDirect(ctx context.Context, raw, endpoint string, audit *core.AuditLogger) (htmlStr, finalURL, via string, err error) {
	c := &bridge.Client{Endpoint: endpoint}
	htmlStr, finalURL, _, err = c.GetHTML(ctx, raw, audit)
	if err == nil {
		return htmlStr, finalURL, "bridge", nil
	}
	if !errors.Is(err, bridge.ErrBridgeUnreachable) && !errors.Is(err, bridge.ErrNoExtensionAttached) {
		// A real extension/timeout error — surface it. Falling back
		// after a navigate that *partly* worked could mask bugs.
		return "", "", "", err
	}
	audit.Logf("falling back to direct HTTP (%v)", err)
	body, err := core.GetBytes(ctx, raw)
	if err != nil {
		return "", "", "", err
	}
	return string(body), raw, "http", nil
}
