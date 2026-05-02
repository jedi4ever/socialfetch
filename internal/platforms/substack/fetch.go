// Package substack fetches Substack posts. Like internal/sources/medium
// it tries the browser bridge first so logged-in member content is
// readable, then falls back to direct HTTP for public excerpts.
package substack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jedi4ever/socialfetch/internal/bridge"
	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/util/htmlmeta"
)

type Fetcher struct {
	BridgeURL string
	Extractor *Extractor
}

func New() *Fetcher {
	return &Fetcher{
		BridgeURL: bridge.DefaultEndpoint,
		Extractor: &Extractor{},
	}
}

func (Fetcher) Name() string { return "substack" }

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	return host == "substack.com" || strings.HasSuffix(host, ".substack.com")
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	htmlStr, finalURL, via, err := bridgeOrDirect(ctx, raw, f.BridgeURL, opts.Audit)
	if err != nil {
		return nil, fmt.Errorf("substack: %w", err)
	}

	page, err := htmlmeta.Parse(bytes.NewReader([]byte(htmlStr)))
	if err != nil {
		return nil, fmt.Errorf("substack: parse: %w", err)
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

// bridgeOrDirect: same behavior as the Medium helper. Kept inline
// (rather than factored out) so each per-host fetcher can tweak its
// own logging prefix or fallback policy independently if it ever
// needs to.
func bridgeOrDirect(ctx context.Context, raw, endpoint string, audit *core.AuditLogger) (htmlStr, finalURL, via string, err error) {
	c := &bridge.Client{Endpoint: endpoint}
	htmlStr, finalURL, _, err = c.GetHTML(ctx, raw, audit)
	if err == nil {
		return htmlStr, finalURL, "bridge", nil
	}
	if !errors.Is(err, bridge.ErrBridgeUnreachable) && !errors.Is(err, bridge.ErrNoExtensionAttached) {
		return "", "", "", err
	}
	audit.Logf("falling back to direct HTTP (%v)", err)
	body, err := core.GetBytes(ctx, raw)
	if err != nil {
		return "", "", "", err
	}
	return string(body), raw, "http", nil
}
