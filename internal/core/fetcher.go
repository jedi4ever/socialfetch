package core

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Fetcher is implemented by every source. Match decides whether this
// fetcher claims a given URL; Fetch returns the populated Item.
//
// Fetchers must be safe to use concurrently and should respect ctx for
// both network timeouts and cancellation.
type Fetcher interface {
	Name() string
	Match(u *url.URL) bool
	Fetch(ctx context.Context, raw string, opts Options) (*Item, error)
}

// Registry holds the ordered list of fetchers. Order matters: the first
// matching fetcher wins, so put more specific sources (hackernews, reddit)
// before generic ones (article, rss).
type Registry struct {
	fetchers []Fetcher
}

// NewRegistry builds a registry from the given fetchers, in order.
func NewRegistry(fetchers ...Fetcher) *Registry {
	return &Registry{fetchers: fetchers}
}

// Resolve returns the first fetcher whose Match accepts the URL.
func (r *Registry) Resolve(raw string) (Fetcher, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", raw, err)
	}
	for _, f := range r.fetchers {
		if f.Match(u) {
			return f, nil
		}
	}
	return nil, fmt.Errorf("no fetcher matched url %q", raw)
}

// Fetch resolves and runs the fetcher in one step.
func (r *Registry) Fetch(ctx context.Context, raw string, opts Options) (*Item, error) {
	f, err := r.Resolve(raw)
	if err != nil {
		return nil, err
	}
	opts.Audit.Logf("fetch %s via %s", raw, f.Name())
	item, err := f.Fetch(ctx, raw, opts)
	if err != nil {
		opts.Audit.Logf("fetch %s FAILED via %s: %v", raw, f.Name(), err)
		return nil, err
	}
	// Stamp the original request URL on the way out, unless the
	// fetcher already set it (some fetchers know better — e.g. an
	// API-backed fetcher that received a short-form ID and wants
	// to record the user's exact input separately). Only set when
	// it differs from the canonical URL the fetcher produced;
	// the JSON `omitempty` then keeps the wire shape identical to
	// before for the no-redirect case.
	if item != nil && item.RequestURL == "" && item.URL != raw {
		item.RequestURL = raw
	}
	opts.Audit.Logf("fetch %s ok via %s bytes=%d comments=%d media=%d",
		raw, f.Name(), len(item.Content), len(item.Comments), len(item.Media))
	return item, nil
}

// Names lists registered fetcher names — used by --help and tests.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.fetchers))
	for _, f := range r.fetchers {
		out = append(out, f.Name())
	}
	return out
}

// Fetchers returns the underlying fetcher list (read-only). Useful for
// rendering a "supported sources" table in --help.
func (r *Registry) Fetchers() []Fetcher {
	out := make([]Fetcher, len(r.fetchers))
	copy(out, r.fetchers)
	return out
}
