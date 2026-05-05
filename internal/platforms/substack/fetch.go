// Package substack fetches Substack posts via the shared fetch chain.
// Default chain is `bridge,http,jina` — same shape as Medium, since
// the paywall + member-only patterns are nearly identical:
//
//   - `bridge` — uses the user's logged-in browser session for
//     subscriber-only posts.
//   - `http`   — direct GET; returns public excerpts for paywalled
//     posts and the full body for free posts.
//   - `jina`   — anonymous catch-all for when both local methods fail.
//
// Operators override per call via SOCIAL_FETCH_CHAIN_SUBSTACK.
package substack

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/bridge"
	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/fetchchain"
	"github.com/jedi4ever/social-skills/internal/render/htmlmd"
	"github.com/jedi4ever/social-skills/internal/util/htmlmeta"
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

var defaultChain = []fetchchain.Method{
	fetchchain.MethodBridge,
	fetchchain.MethodHTTP,
	fetchchain.MethodJina,
}

var supportedMethods = map[fetchchain.Method]bool{
	fetchchain.MethodBridge: true,
	fetchchain.MethodHTTP:   true,
	fetchchain.MethodJina:   true,
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	chain := fetchchain.Resolve(fetchchain.FromEnv("substack"), defaultChain, supportedMethods)
	runners := map[fetchchain.Method]fetchchain.Runner[*core.Item]{
		fetchchain.MethodBridge: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaBridge(ctx, raw, opts)
		},
		fetchchain.MethodHTTP: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaHTTP(ctx, raw, opts)
		},
		fetchchain.MethodJina: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaJina(ctx, raw, opts)
		},
	}
	item, _, err := fetchchain.Run(ctx, "substack", raw, opts.Audit, chain, runners)
	if err != nil {
		return nil, fmt.Errorf("substack: %w", err)
	}
	return item, nil
}

func (f *Fetcher) fetchViaBridge(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	c := &bridge.Client{Endpoint: f.BridgeURL}
	htmlStr, finalURL, _, err := c.GetHTML(ctx, raw, opts.Audit)
	if err != nil {
		return nil, err
	}
	return f.parseAndExtract(raw, finalURL, []byte(htmlStr), "bridge")
}

func (f *Fetcher) fetchViaHTTP(ctx context.Context, raw string, _ core.Options) (*core.Item, error) {
	body, err := core.GetBytes(ctx, raw)
	if err != nil {
		return nil, err
	}
	return f.parseAndExtract(raw, raw, body, "http")
}

// fetchViaJina is the anonymous catch-all. Surfaces title / URL /
// description from Jina's envelope; the body itself comes back as
// pre-cleaned markdown.
func (f *Fetcher) fetchViaJina(ctx context.Context, raw string, _ core.Options) (*core.Item, error) {
	res, err := htmlmd.NewJinaReader().ReadFull(ctx, raw)
	if err != nil {
		return nil, err
	}
	finalURL := raw
	if res.URL != "" {
		finalURL = res.URL
	}
	return &core.Item{
		Source:      "substack",
		Kind:        "article",
		URL:         finalURL,
		CanonicalID: finalURL,
		Title:       res.Title,
		Summary:     res.Description,
		Content:     strings.TrimSpace(res.Content),
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"requested_url": raw,
			"via":           "jina",
		},
	}, nil
}

func (f *Fetcher) parseAndExtract(raw, finalURL string, body []byte, via string) (*core.Item, error) {
	page, err := htmlmeta.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
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
