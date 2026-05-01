package timeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"
)

// XSearcher is the subset of search.Provider this package needs from
// xsearch. Defining it locally lets the test substitute a fake without
// touching the real X API.
type XSearcher interface {
	Search(ctx context.Context, query string, opts search.Options) ([]search.Result, error)
}

// XProvider implements Provider for X by wrapping a recent-search
// query of `from:<user>` with optional kind filters. It carries no
// auth itself; the underlying searcher reads X_API_KEY/SECRET.
type XProvider struct {
	Searcher XSearcher
}

func NewXProvider(s XSearcher) *XProvider { return &XProvider{Searcher: s} }

func (XProvider) Name() string { return "x" }

func (p XProvider) Fetch(ctx context.Context, user string, opts Options) (*core.Item, error) {
	if user == "" {
		return nil, fmt.Errorf("x timeline: empty user")
	}
	q := "from:" + user
	switch strings.ToLower(opts.Kind) {
	case "", "all":
		// no extra operator — returns tweets, replies, retweets
	case "tweets", "posts":
		q += " -is:reply -is:retweet"
	case "replies":
		q += " is:reply"
	case "retweets":
		q += " is:retweet"
	default:
		return nil, fmt.Errorf("x timeline: unknown kind %q (want all|tweets|replies|retweets)", opts.Kind)
	}

	max := opts.Max
	if max <= 0 {
		max = 30
	}

	// X's recent-search caps history at 7 days. Default to that window
	// when no --after is supplied so users don't get an empty response
	// when X clamps internally.
	after := opts.After
	if after == nil {
		t := time.Now().Add(-7 * 24 * time.Hour).Add(time.Minute)
		after = &t
	}

	opts.Audit.Logf("timeline x: %q (max=%d)", q, max)
	results, err := p.Searcher.Search(ctx, q, search.Options{
		Max:    max,
		After:  after,
		Before: opts.Before,
	})
	if err != nil {
		return nil, err
	}

	children := make([]core.Item, 0, len(results))
	for _, r := range results {
		children = append(children, core.Item{
			Source:    "x",
			Kind:      kindForX(opts.Kind),
			URL:       r.URL,
			Title:     firstLine(r.Snippet, 80),
			Author:    strings.TrimPrefix(r.Title, "@"),
			Summary:   r.Snippet,
			Published: r.Published,
		})
	}

	profileURL := "https://x.com/" + user
	return &core.Item{
		Source:    "x",
		Kind:      "timeline",
		URL:       profileURL,
		Title:     "Timeline of @" + user,
		Author:    user,
		AuthorURL: profileURL,
		Children:  children,
		FetchedAt: time.Now().UTC(),
		Extra: map[string]any{
			"user":  user,
			"kind":  defaultKind(opts.Kind),
			"count": len(children),
		},
	}, nil
}

func kindForX(k string) string {
	switch strings.ToLower(k) {
	case "tweets", "posts":
		return "tweet"
	case "replies":
		return "reply"
	case "retweets":
		return "retweet"
	default:
		return "tweet"
	}
}

func defaultKind(k string) string {
	if k == "" {
		return "all"
	}
	return strings.ToLower(k)
}

// firstLine clips a snippet to the first newline (or maxRunes if no
// newline) so timeline children render with a reasonable Title.
// Markdown link syntax `[text](url)` is collapsed to `text` first so we
// count visible characters, not URL noise.
func firstLine(s string, maxRunes int) string {
	s = stripMarkdownLinks(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > maxRunes {
		r := []rune(s)
		return string(r[:maxRunes-1]) + "…"
	}
	return s
}

// stripMarkdownLinks collapses `[label](url)` to `label`. Used for
// title generation so a body full of LinkedIn @-mention links doesn't
// blow the budget rendering URLs nobody reads in a header.
func stripMarkdownLinks(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '[' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Find matching `](` and `)` — keep this loose; on no match we
		// just emit the original `[`.
		closeBracket := strings.Index(s[i:], "](")
		if closeBracket < 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		closeBracket += i
		closeParen := strings.Index(s[closeBracket:], ")")
		if closeParen < 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		closeParen += closeBracket
		b.WriteString(s[i+1 : closeBracket])
		i = closeParen + 1
	}
	return b.String()
}
