package notifier

// registry.go — process-wide catalog of registered providers.
// Mirrors internal/agent/harness's registry shape: each provider
// file's init() calls Register; CLI + MCP surfaces look up by
// name. Keeps the dispatch surface flat — no big switch statement
// to maintain when a new provider lands.

import (
	"fmt"
	"sort"
	"strings"
)

var registry = map[string]Provider{}

// Register adds p under p.Name(). Panics on duplicate name to
// surface a programming error at startup rather than letting one
// provider silently shadow another. Called from each provider's
// init().
func Register(p Provider) {
	name := strings.ToLower(p.Name())
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("notifier: duplicate provider %q", name))
	}
	registry[name] = p
}

// Get returns the provider registered under name (case-insensitive)
// or an error listing what is registered. Empty name resolves to
// the first-registered provider (today: slack) so plain
// `social-notifier post …` works zero-config when only one
// provider is wired.
func Get(name string) (Provider, error) {
	if name == "" {
		names := Names()
		if len(names) == 0 {
			return nil, fmt.Errorf("no notifier providers registered")
		}
		return registry[names[0]], nil
	}
	if p, ok := registry[strings.ToLower(name)]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unknown notifier provider %q (try: %s)", name, strings.Join(Names(), " | "))
}

// Names returns every registered provider, sorted alphabetically.
// Used by `social-notifier providers list` and by Get's error
// message.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
