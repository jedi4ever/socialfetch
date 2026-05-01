// Package timeline fetches a user's recent activity from a social
// platform (X tweets, LinkedIn posts/comments/reactions) and returns it
// as a single core.Item with one child Item per activity. Markdown
// rendering of the umbrella item shows the user's profile metadata; the
// children render via the existing renderChildEntry path.
//
// Each platform is implemented as a Provider; the package picks one
// based on a CLI hint or a URL host. X timelines wrap the existing
// xsearch package (no extra auth, hard 7-day window). LinkedIn
// timelines drive the local browser-extension bridge to navigate the
// /in/<user>/recent-activity/... pages and scrape the rendered DOM.
package timeline

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

// Options shape one timeline call. Kind values are provider-specific;
// "all" is the cross-provider default. Max caps the number of children
// returned and is best-effort: LinkedIn is bounded by what the first
// page renders, X is bounded by recent-search results.
type Options struct {
	Kind   string     // all (default), and provider-specific kinds
	Max    int        // result cap (default 30)
	After  *time.Time // earliest item; X enforces 7-day window
	Before *time.Time // latest item
	Expand bool       // LinkedIn: re-fetch each item via the post fetcher (slow)
	Audit  *core.AuditLogger
}

// Provider implements timeline lookup for one platform.
type Provider interface {
	Name() string
	Fetch(ctx context.Context, user string, opts Options) (*core.Item, error)
}

// Registry indexes providers by name.
type Registry struct{ providers []Provider }

func NewRegistry(providers ...Provider) *Registry {
	return &Registry{providers: providers}
}

func (r *Registry) Get(name string) (Provider, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range r.providers {
		if strings.ToLower(p.Name()) == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown timeline provider %q (known: %s)", name, strings.Join(r.Names(), ", "))
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Name())
	}
	return out
}

// ParseIdentifier resolves the user's input into (provider, user). It
// accepts:
//
//   - "swyx"                    + hint "x"        -> ("x", "swyx")
//   - "@swyx"                                     -> ("x", "swyx")    (@ implies X)
//   - "https://x.com/swyx"                        -> ("x", "swyx")
//   - "https://twitter.com/swyx/status/..."       -> ("x", "swyx")
//   - "https://www.linkedin.com/in/patrickdebois" -> ("linkedin", "patrickdebois")
//   - "patrickdebois"           + hint "linkedin" -> ("linkedin", "patrickdebois")
//
// A bare handle with no hint defaults to X (the more common case).
func ParseIdentifier(input, providerHint string) (provider, user string, err error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", "", errors.New("empty identifier")
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, perr := url.Parse(s)
		if perr != nil {
			return "", "", perr
		}
		host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
		switch {
		case host == "x.com" || host == "twitter.com":
			handle := strings.Trim(u.Path, "/")
			if i := strings.Index(handle, "/"); i >= 0 {
				handle = handle[:i]
			}
			if handle == "" {
				return "", "", fmt.Errorf("no handle in %s", s)
			}
			return "x", handle, nil
		case host == "linkedin.com" || strings.HasSuffix(host, ".linkedin.com"):
			parts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 3)
			if len(parts) >= 2 && parts[0] == "in" {
				return "linkedin", parts[1], nil
			}
			return "", "", fmt.Errorf("LinkedIn URL must be /in/<user>/...: %s", s)
		}
		return "", "", fmt.Errorf("unrecognised host %q (want x.com or linkedin.com)", host)
	}
	if strings.HasPrefix(s, "@") {
		return "x", strings.TrimPrefix(s, "@"), nil
	}
	if providerHint == "" {
		return "x", s, nil
	}
	return strings.ToLower(strings.TrimSpace(providerHint)), s, nil
}
