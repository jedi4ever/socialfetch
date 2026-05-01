package timeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/sources/linkedin"
)

// LinkedInProvider implements Provider for LinkedIn by driving the
// browser-extension bridge through /in/<user>/recent-activity/<kind>/
// and scraping each card. Bridge required (LinkedIn returns nothing
// useful to anonymous fetches).
type LinkedInProvider struct {
	BridgeURL string // overridable for tests; "" -> bridge.DefaultEndpoint
}

func NewLinkedInProvider() *LinkedInProvider {
	return &LinkedInProvider{BridgeURL: bridge.DefaultEndpoint}
}

func (LinkedInProvider) Name() string { return "linkedin" }

func (p LinkedInProvider) Fetch(ctx context.Context, user string, opts Options) (*core.Item, error) {
	if user == "" {
		return nil, fmt.Errorf("linkedin timeline: empty user")
	}
	kind := linkedin.TimelineKind(strings.ToLower(opts.Kind))
	switch kind {
	case "", linkedin.TimelineAll, linkedin.TimelinePosts, "shares",
		linkedin.TimelineComments, linkedin.TimelineReactions:
		// ok
	default:
		return nil, fmt.Errorf("linkedin timeline: unknown kind %q (want all|posts|comments|reactions)", opts.Kind)
	}
	max := opts.Max
	if max <= 0 {
		max = 30
	}

	opts.Audit.Logf("timeline linkedin: %s/%s (max=%d)", user, string(kind), max)
	acts, target, err := linkedin.FetchTimeline(ctx, p.BridgeURL, user, kind, max, opts.Audit)
	if err != nil {
		return nil, err
	}

	children := make([]core.Item, 0, len(acts))
	for _, a := range acts {
		// Apply --after/--before filtering when LinkedIn gave us a
		// resolved Published time; if not, keep the item (we'd rather
		// show too much than silently drop unstamped activity).
		if a.Published != nil {
			if opts.After != nil && a.Published.Before(*opts.After) {
				continue
			}
			if opts.Before != nil && a.Published.After(*opts.Before) {
				continue
			}
		}

		child := core.Item{
			Source:      "linkedin",
			Kind:        childKindForLinkedIn(kind, a.Header),
			URL:         a.URL,
			CanonicalID: a.URN,
			Title:       firstLine(a.Body, 80),
			Author:      user,
			AuthorURL:   "https://www.linkedin.com/in/" + user,
			Summary:     a.Body,
			Published:   a.Published,
			Extra: map[string]any{
				"rel_time": a.RelTime,
				"header":   a.Header,
			},
		}
		children = append(children, child)
	}

	profileURL := "https://www.linkedin.com/in/" + user + "/"
	return &core.Item{
		Source:    "linkedin",
		Kind:      "timeline",
		URL:       target, // the recent-activity URL we actually navigated to
		Title:     "Timeline of " + user,
		Author:    user,
		AuthorURL: profileURL,
		Children:  children,
		FetchedAt: time.Now().UTC(),
		Extra: map[string]any{
			"user":  user,
			"kind":  defaultKind(opts.Kind),
			"count": len(children),
			"via":   "bridge",
			"note":  "first-page only; LinkedIn lazy-loads further activity on scroll",
		},
	}, nil
}

func childKindForLinkedIn(kind linkedin.TimelineKind, header string) string {
	// If LinkedIn rendered an explicit header ("Patrick Debois reposted
	// this", "...commented on this"), surface its semantic prefix.
	low := strings.ToLower(header)
	switch {
	case strings.Contains(low, "reposted"):
		return "repost"
	case strings.Contains(low, "commented"):
		return "comment"
	case strings.Contains(low, "liked") || strings.Contains(low, "celebrates") || strings.Contains(low, "reacted"):
		return "reaction"
	}
	switch kind {
	case linkedin.TimelinePosts, "shares":
		return "post"
	case linkedin.TimelineComments:
		return "comment"
	case linkedin.TimelineReactions:
		return "reaction"
	}
	return "post"
}
