// Package linkedin fetches a LinkedIn post via a configurable chain
// of methods. Default chain is `bridge,jina`:
//
//   - `bridge` — local browser-extension routes the URL through the
//     user's logged-in session. Best fidelity (full body + comments
//   - media tree) but requires the bridge daemon running.
//   - `jina` — anonymous fallback via r.jina.ai. Returns LinkedIn's
//     guest-preview body (full prose, author, reaction count) but
//     no comment thread (LinkedIn's auth wall hides it).
//
// Operators override per-call via SOCIAL_FETCH_CHAIN_LINKEDIN
// (e.g. `SOCIAL_FETCH_CHAIN_LINKEDIN=jina` for always-anonymous,
// `SOCIAL_FETCH_CHAIN_LINKEDIN=bridge` for bridge-only legacy
// behaviour).
package linkedin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/jedi4ever/social-skills/internal/bridge"
	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/fetchchain"
	renderhtmlmd "github.com/jedi4ever/social-skills/internal/render/htmlmd"
	"github.com/jedi4ever/social-skills/internal/util/htmlmd"
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

// defaultChain is the order LinkedIn tries when no env override is
// set. Bridge first because it returns the highest-fidelity result
// (comments, media tree, full reactions). Jina is the anonymous
// fallback — public-preview body without comments, used when the
// bridge is down / timed out / not configured.
var defaultChain = []fetchchain.Method{fetchchain.MethodBridge, fetchchain.MethodJina}

// supported lists the methods this fetcher knows how to execute.
// Extra entries in the env var get filtered out via fetchchain.Resolve
// so an operator's typo doesn't accidentally disable the fetcher.
var supported = map[fetchchain.Method]bool{
	fetchchain.MethodBridge: true,
	fetchchain.MethodJina:   true,
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	chain := fetchchain.Resolve(fetchchain.FromEnv("linkedin"), defaultChain, supported)
	runners := map[fetchchain.Method]fetchchain.Runner[*core.Item]{
		fetchchain.MethodBridge: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaBridge(ctx, raw, opts)
		},
		fetchchain.MethodJina: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaJina(ctx, raw, opts)
		},
	}
	item, _, err := fetchchain.Run(ctx, "linkedin", raw, opts.Audit, chain, runners)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// fetchViaBridge is the original logged-in path. Returns the richest
// possible Item: full body via cleanHTML+convert+trim, comment tree,
// media slice, author/profile URL.
//
// Errors map to the same friendly messages we shipped before so an
// operator with the bridge-only chain still sees actionable advice.
func (f *Fetcher) fetchViaBridge(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	client := &bridge.Client{Endpoint: f.BridgeURL}
	htmlStr, finalURL, title, err := client.GetHTML(ctx, raw, opts.Audit)
	if err != nil {
		switch {
		case errors.Is(err, bridge.ErrBridgeUnreachable):
			return nil, fmt.Errorf("bridge daemon not running — `social-fetch bridge start`: %w", err)
		case errors.Is(err, bridge.ErrBridgeTimeout):
			return nil, fmt.Errorf("bridge timed out loading the page — LinkedIn can be slow; try again or set SOCIAL_BRIDGE_TIMEOUT=180s for headroom: %w", err)
		case errors.Is(err, bridge.ErrNoExtensionAttached):
			return nil, fmt.Errorf("no extension attached — open your browser with the social-fetch extension running")
		default:
			return nil, err
		}
	}

	if finalURL == "" {
		finalURL = raw
	}
	cleanedHTML, comments := cleanHTML(htmlStr)
	body2 := trimBoilerplate(htmlmd.Convert(cleanedHTML))

	// Media extraction runs against the RAW HTML (not cleanedHTML),
	// because cleanHTML strips images aggressively to keep the body
	// text-focused.
	var media []core.Media
	if doc, perr := html.Parse(strings.NewReader(htmlStr)); perr == nil {
		media = extractMedia(doc)
	}

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
		Media:       media,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"via":           "bridge",
			"comment_count": len(comments),
		},
	}, nil
}

// fetchViaJina is the anonymous fallback. Routes the URL through
// r.jina.ai which renders LinkedIn's guest-preview page in a
// headless browser and returns clean markdown. What we get vs the
// bridge path:
//
//	field         | bridge       | jina
//	------------- | ------------ | --------------------------
//	body          | full          | full (guest-preview prose)
//	author        | parsed        | parsed from "Name | LinkedIn" title
//	comments      | full thread   | always nil (auth-walled)
//	media         | structured    | inline ![](url) only, no Media[]
//	reaction list | full          | not available
//
// Comments get a single audit-log line so an agent reading the trace
// knows why they're empty (it's the platform, not a bug).
func (f *Fetcher) fetchViaJina(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	md, err := renderhtmlmd.NewJinaReader().Read(ctx, raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(md) == "" {
		return nil, fmt.Errorf("empty markdown from Jina for %s", raw)
	}

	title, author, body := parseJinaLinkedInOutput(md)

	if opts.Audit != nil {
		opts.Audit.Logf("linkedin: jina path returned %d chars (no comments — anonymous mode)", len(body))
	}

	return &core.Item{
		Source:      "linkedin",
		Kind:        kindFor(raw),
		URL:         raw,
		CanonicalID: canonicalID(raw),
		Title:       title,
		Author:      author,
		Content:     body,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"via":           "jina",
			"comment_count": 0,
			"anonymous":     true,
		},
	}, nil
}

// parseJinaLinkedInOutput pulls out title / author / body from the
// markdown Jina returns for a LinkedIn URL. Jina's output starts
// with `Title: <Page title>` then a `URL Source:` line, then
// `Markdown Content:` and the body. LinkedIn page titles read
// "<Post title or first words> | <Author Name> posted on the topic
// | LinkedIn", so author comes out of the title.
//
// All extraction is best-effort: missing fields stay empty rather
// than failing the fetch.
func parseJinaLinkedInOutput(md string) (title, author, body string) {
	lines := strings.SplitN(md, "\n", 50)
	bodyStart := 0
	for i, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "Title: "):
			title = strings.TrimSpace(strings.TrimPrefix(ln, "Title: "))
		case strings.HasPrefix(ln, "Markdown Content:"):
			bodyStart = i + 1
		}
	}
	// Author parsing: "Foo bar | Cole Medin posted on the topic | LinkedIn"
	if title != "" {
		if author = parseAuthorFromTitle(title); author != "" {
			// Strip the "| Author Name posted... | LinkedIn"
			// suffix from the title so the visible Title field
			// is the post's first words rather than chrome.
			if i := strings.Index(title, " | "); i > 0 {
				title = strings.TrimSpace(title[:i])
			}
		}
	}
	if bodyStart > 0 && bodyStart < len(lines) {
		body = strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))
	} else {
		body = strings.TrimSpace(md)
	}
	return title, author, body
}

// parseAuthorFromTitle extracts the author name from Jina's
// LinkedIn-shaped title string. Patterns we see in real output:
//
//	"<headline> | <Author Name> posted on the topic | LinkedIn"
//	"<headline> | <Author Name> on LinkedIn: <snippet>"
//	"(N) Post | LinkedIn" — no author parseable
//
// The first pattern dominates; we strip "posted on the topic" and
// the "| LinkedIn" trailer to land on a clean name.
func parseAuthorFromTitle(title string) string {
	parts := strings.Split(title, " | ")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "LinkedIn" || strings.HasPrefix(p, "(") {
			continue
		}
		// "Cole Medin posted on the topic" → "Cole Medin"
		for _, suffix := range []string{
			" posted on the topic",
			" on LinkedIn",
		} {
			if i := strings.Index(p, suffix); i > 0 {
				return strings.TrimSpace(p[:i])
			}
		}
	}
	return ""
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
