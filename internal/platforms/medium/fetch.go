// Package medium fetches Medium articles via a configurable fetch
// chain. Default order is `bridge,http,jina`:
//
//   - `bridge` — local browser bridge routes through the user's
//     logged-in Medium session so member-only paywalls open.
//   - `http`   — direct GET. Returns the public excerpt for paywalled
//     posts and the full body for free posts.
//   - `jina`   — anonymous catch-all via r.jina.ai. Used when both
//     bridge and direct HTTP fail (rate limiting, anti-bot, regional
//     blocks).
//
// In all three cases the resulting HTML / markdown is run through the
// MediumExtractor so the rendered output shape stays consistent —
// agents reading `Extra["via"]` know which method actually produced
// the body, but the rest of the Item shape is identical.
//
// Operators override the chain via SOCIAL_FETCH_CHAIN_MEDIUM
// (e.g. `SOCIAL_FETCH_CHAIN_MEDIUM=http,jina` to skip the bridge
// entirely, or `SOCIAL_FETCH_CHAIN_MEDIUM=jina` for always-anonymous).
package medium

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

func (Fetcher) Name() string { return "medium" }

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	return host == "medium.com" || strings.HasSuffix(host, ".medium.com")
}

// defaultChain — bridge first because Medium's paywall makes the
// logged-in path strictly better when available; HTTP next for the
// free-tier excerpt path; Jina last as anonymous catch-all.
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
	chain := fetchchain.Resolve(fetchchain.FromEnv("medium"), defaultChain, supportedMethods)
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
	item, _, err := fetchchain.Run(ctx, "medium", raw, opts.Audit, chain, runners)
	if err != nil {
		return nil, fmt.Errorf("medium: %w", err)
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

// fetchViaJina is the anonymous catch-all. Jina pre-cleans the page
// to markdown so we skip the htmlmeta+extractor pipeline and return
// a body-only Item — losing structured Author / Published / Tags /
// Media metadata in exchange for getting *something* when the local
// methods fail.
func (f *Fetcher) fetchViaJina(ctx context.Context, raw string, _ core.Options) (*core.Item, error) {
	md, err := htmlmd.NewJinaReader().Read(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &core.Item{
		Source:      "medium",
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
