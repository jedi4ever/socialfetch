// Package search defines the Provider interface that backends — DuckDuckGo,
// SerpAPI, others — implement. A Result is intentionally tiny: just enough
// to feed back into the fetch pipeline.
package search

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Result is one search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Source  string `json:"source"`
}

// Options shape a single search call. Date and domain filters are
// best-effort: providers that don't support a native filter ignore it;
// providers with coarse granularity (Tavily's "last N days") round to
// the closest supported window.
type Options struct {
	Max            int        // max results; <=0 means provider default
	Before         *time.Time // only results published before this time
	After          *time.Time // only results published after this time
	IncludeDomains []string   // allowlist; if non-empty, restrict to these
	ExcludeDomains []string   // denylist
}

// DefaultOptions returns options with the provider's own defaults.
func DefaultOptions() Options { return Options{} }

// Provider performs queries against a backend.
type Provider interface {
	Name() string
	Search(ctx context.Context, query string, opts Options) ([]Result, error)
}

// Registry holds the known search providers.
type Registry struct {
	providers []Provider
}

func NewRegistry(providers ...Provider) *Registry {
	return &Registry{providers: providers}
}

// Get returns the named provider, or an error listing the known names.
func (r *Registry) Get(name string) (Provider, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range r.providers {
		if strings.ToLower(p.Name()) == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown search provider %q (known: %s)", name, strings.Join(r.Names(), ", "))
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Name())
	}
	return out
}

func (r *Registry) Providers() []Provider {
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}
