package linkedin

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/timeline"
)

// linkedinPostFetcher abstracts the per-post LinkedIn fetcher so the
// expand path can be tested without driving the real bridge.
type linkedinPostFetcher interface {
	Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error)
}

// LinkedInProvider implements Provider for LinkedIn by driving the
// browser-extension bridge through /in/<user>/recent-activity/<kind>/
// and scraping each card. Bridge required (LinkedIn returns nothing
// useful to anonymous fetches).
//
// When Options.Expand is true, every child post is re-fetched through
// PostFetcher (the standard LinkedIn post fetcher) so the result
// includes the full body, comment tree, and richer metadata. This is
// serial — the bridge has one tab — and slow (~3s per post), so it's
// strictly opt-in.
type LinkedInProvider struct {
	BridgeURL   string              // overridable for tests; "" -> bridge.DefaultEndpoint
	PostFetcher linkedinPostFetcher // overridable for tests; defaults to New()
}

func NewLinkedInProvider() *LinkedInProvider {
	return &LinkedInProvider{
		BridgeURL:   bridge.DefaultEndpoint,
		PostFetcher: New(),
	}
}

func (LinkedInProvider) Name() string { return "linkedin" }

func (p LinkedInProvider) Fetch(ctx context.Context, user string, opts timeline.Options) (*core.Item, error) {
	if user == "" {
		return nil, fmt.Errorf("linkedin timeline: empty user")
	}
	kind := TimelineKind(strings.ToLower(opts.Kind))
	switch kind {
	case "", TimelineAll, TimelinePosts, "shares",
		TimelineComments, TimelineReactions:
		// ok
	default:
		return nil, fmt.Errorf("linkedin timeline: unknown kind %q (want all|posts|comments|reactions)", opts.Kind)
	}
	max := opts.Max
	if max <= 0 {
		max = 30
	}

	opts.Audit.Logf("timeline linkedin: %s/%s (max=%d)", user, string(kind), max)
	acts, target, err := FetchTimeline(ctx, p.BridgeURL, user, kind, max, opts.Audit)
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
		if opts.ExcludeShares && isShare(a.Header) {
			opts.Audit.Logf("linkedin timeline: skip reshare %s", a.URN)
			continue
		}

		child := core.Item{
			Source:      "linkedin",
			Kind:        childKindForLinkedIn(kind, a.Header),
			URL:         a.URL,
			CanonicalID: a.URN,
			Title:       firstLine(a.Body, 120),
			Author:      user,
			AuthorURL:   "https://www.linkedin.com/in/" + user,
			Summary:     a.Body,
			Published:   a.Published,
			Extra: map[string]any{
				"rel_time": a.RelTime,
				"header":   a.Header,
			},
		}

		if opts.Expand && p.PostFetcher != nil {
			// Plausibly-human pause between consecutive post navigations
			// so the request cadence doesn't look like a scripted bot.
			// Skip the pause on the first item — there's nothing to space
			// out from.
			if len(children) > 0 {
				select {
				case <-time.After(randExpandDelay()):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			full, err := p.PostFetcher.Fetch(ctx, a.URL, core.Options{
				IncludeComments: true,
				Audit:           opts.Audit,
			})
			if err != nil {
				opts.Audit.Logf("linkedin expand %s FAILED: %v", a.URN, err)
			} else {
				// Keep the timeline-derived metadata (kind, rel_time,
				// resolved Published from the relative stamp) and graft
				// in the full post's content + comments.
				child.Content = full.Content
				child.Comments = full.Comments
				if full.Title != "" && child.Title == "" {
					child.Title = full.Title
				}
				if full.AuthorURL != "" {
					child.AuthorURL = full.AuthorURL
				}
				child.Extra["expanded"] = true
				child.Extra["comment_count"] = len(full.Comments)
			}
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

// randExpandDelay returns a randomized pause between consecutive
// per-post deep fetches in --expand mode. Distribution:
//
//   - ~70%: 1.8–4.5s — the "skim and click" pace
//   - ~25%: 5–9s     — pausing to read a longer post
//   - ~5%:  10–15s   — distracted / multitasking pause
//
// The dispersion mimics organic browsing without being slow enough to
// kill throughput on a 30-item timeline.
func randExpandDelay() time.Duration {
	r := rand.IntN(100)
	switch {
	case r < 70:
		return time.Duration(1800+rand.IntN(2700)) * time.Millisecond
	case r < 95:
		return time.Duration(5000+rand.IntN(4000)) * time.Millisecond
	default:
		return time.Duration(10000+rand.IntN(5000)) * time.Millisecond
	}
}

// isShare reports whether the activity is a reshare/repost based on the
// "X reposted this" / "X reshared this" / "X shared this" card header
// LinkedIn injects above embedded content. Used by the --no-reshares
// filter so users can keep timelines focused on original output.
func isShare(header string) bool {
	low := strings.ToLower(header)
	return strings.Contains(low, "reposted") ||
		strings.Contains(low, "reshared") ||
		strings.Contains(low, " shared this")
}

func childKindForLinkedIn(kind TimelineKind, header string) string {
	// If LinkedIn rendered an explicit header ("Patrick Debois reposted
	// this", "...commented on this"), surface its semantic prefix.
	low := strings.ToLower(header)
	switch {
	case strings.Contains(low, "reposted") || strings.Contains(low, "reshared") || strings.Contains(low, " shared this"):
		return "repost"
	case strings.Contains(low, "commented") || strings.Contains(low, "replied"):
		return "comment"
	case strings.Contains(low, "liked") || strings.Contains(low, "celebrates") || strings.Contains(low, "reacted"):
		return "reaction"
	}
	switch kind {
	case TimelinePosts, "shares":
		return "post"
	case TimelineComments:
		return "comment"
	case TimelineReactions:
		return "reaction"
	}
	return "post"
}

// defaultKind normalises an empty Kind to 'all' so audit logs stay
// stable across explicit and implicit kind selection.
func defaultKind(k string) string {
	if k == "" {
		return "all"
	}
	return strings.ToLower(k)
}
