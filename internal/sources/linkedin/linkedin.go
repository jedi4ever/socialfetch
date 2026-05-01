// Package linkedin fetches a LinkedIn post by routing through the local
// bridge (internal/bridge) — the user's logged-in browser does the
// actual page render, then the bridge streams the resulting HTML back.
//
// Why a bridge instead of a direct HTTP fetch? LinkedIn's public
// endpoints don't return post content without an authenticated session,
// and JavaScript-rendered DOM is required for the post body. The
// extension running in the user's logged-in browser handles both.
package linkedin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/htmlmd"
)

// DefaultBridgeURL is the local bridge endpoint the fetcher POSTs to.
// Override for tests via Fetcher.BridgeURL.
const DefaultBridgeURL = bridge.DefaultEndpoint

type Fetcher struct {
	BridgeURL string
}

func New() *Fetcher { return &Fetcher{BridgeURL: DefaultBridgeURL} }

func (Fetcher) Name() string { return "linkedin" }

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if host != "linkedin.com" && !strings.HasSuffix(host, ".linkedin.com") {
		return false
	}
	p := u.Path
	return strings.Contains(p, "/posts/") ||
		strings.Contains(p, "/feed/update/") ||
		strings.HasPrefix(p, "/in/") ||
		strings.HasPrefix(p, "/pulse/")
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	client := &bridge.Client{Endpoint: f.BridgeURL}
	htmlStr, finalURL, title, err := client.GetHTML(ctx, raw, opts.Audit)
	if err != nil {
		switch {
		case errors.Is(err, bridge.ErrBridgeUnreachable):
			return nil, fmt.Errorf("linkedin: bridge daemon not running — `socialfetch bridge start`: %w", err)
		case errors.Is(err, bridge.ErrNoExtensionAttached):
			return nil, fmt.Errorf("linkedin: no extension attached — open your browser with the PatAI extension running")
		default:
			return nil, fmt.Errorf("linkedin: %w", err)
		}
	}

	if finalURL == "" {
		finalURL = raw
	}
	cleanedHTML, comments := cleanHTML(htmlStr)
	body2 := trimBoilerplate(htmlmd.Convert(cleanedHTML))

	author, authorURL := extractAuthor(htmlStr)
	canonical := canonicalID(finalURL)

	return &core.Item{
		Source:      "linkedin",
		Kind:        kindFor(finalURL),
		URL:         finalURL,
		CanonicalID: canonical,
		Title:       firstLine(pickFirst(title, body2), 120),
		Author:      author,
		AuthorURL:   authorURL,
		Content:     body2,
		Comments:    comments,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"via":           "bridge",
			"comment_count": len(comments),
		},
	}, nil
}

// canonicalID pulls the activity / ugcPost numeric id out of a LinkedIn
// URL when present, so dedup and JSON consumers have a stable key.
var idRE = regexp.MustCompile(`(?:activity|ugcPost)[-:](\d{15,})`)

func canonicalID(raw string) string {
	if m := idRE.FindStringSubmatch(raw); len(m) >= 2 {
		return m[1]
	}
	return ""
}

func kindFor(raw string) string {
	switch {
	case strings.Contains(raw, "/in/"):
		return "profile"
	case strings.Contains(raw, "/pulse/"):
		return "article"
	default:
		return "post"
	}
}

// extractAuthor pulls the most useful author signal we can from the raw
// HTML without parsing the whole document. LinkedIn injects an
// `og:title` plus an actor-name span; we look for both and pick the
// first that yields a sensible value. Returns (display name, profile URL).
var ogTitleRE = regexp.MustCompile(`<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)["']`)
var actorURLRE = regexp.MustCompile(`href=["']([^"']*linkedin\.com/in/[^"'?]+)`)

func extractAuthor(html string) (name, profile string) {
	if m := ogTitleRE.FindStringSubmatch(html); len(m) >= 2 {
		// og:title looks like "Jane Doe on LinkedIn: …"; trim the suffix.
		name = strings.TrimSpace(m[1])
		if i := strings.Index(name, " on LinkedIn"); i > 0 {
			name = name[:i]
		}
	}
	if m := actorURLRE.FindStringSubmatch(html); len(m) >= 2 {
		profile = strings.TrimRight(m[1], "/")
		if !strings.HasPrefix(profile, "http") {
			profile = "https://www." + strings.TrimPrefix(profile, "//")
		}
	}
	return
}

// trimBoilerplate drops the LinkedIn chrome (cookie banners, "Sign in to
// view more", CTA buttons) that survive cleanHTML because they live in
// regular markup with no class signal. We strip matching substrings,
// drop empty markdown anchors, and collapse repeated lines.
func trimBoilerplate(md string) string {
	deny := []string{
		"Sign in to view more",
		"Skip to main content",
		"Cookie Policy",
		"User Agreement",
		"Privacy Policy",
		"Continue with Google",
		"Join now",
		"New to LinkedIn?",
		"Add section",
		"View my services",
		"Create a post",
		"Contact info",
		"Cover photo",
		"More",
		"Sign in",
	}
	out := md
	for _, d := range deny {
		out = strings.ReplaceAll(out, d, "")
	}

	// Drop empty-text markdown anchors: "[](url)" and "[](#)".
	out = emptyAnchorRE.ReplaceAllString(out, "")

	// Strip leading whitespace on each line — htmlmd preserves the
	// nested-div indentation LinkedIn uses, which produces lines that
	// start with 6-12 spaces. Markdown only treats indentation as
	// meaningful inside code blocks (which we don't have here).
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
		// Don't strip leading whitespace on list items / blockquotes —
		// those are syntactically meaningful.
		trim := strings.TrimLeft(lines[i], " \t")
		if strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ") ||
			strings.HasPrefix(trim, "> ") || strings.HasPrefix(trim, "#") {
			lines[i] = trim
		} else {
			lines[i] = strings.TrimLeft(lines[i], " \t")
		}
	}

	// Dedup adjacent identical non-empty lines (LinkedIn often renders
	// the same CTA two or three times in the same card).
	deduped := lines[:0]
	var prev string
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && t == prev {
			continue
		}
		deduped = append(deduped, l)
		prev = strings.TrimSpace(l)
	}
	out = strings.Join(deduped, "\n")

	// Collapse runs of blank lines left behind.
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out)
}

var emptyAnchorRE = regexp.MustCompile(`\[\]\([^)]*\)`)

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

func pickFirst(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
