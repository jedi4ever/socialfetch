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
	"github.com/jedi4ever/social-skills/internal/render/headless"
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
// set. Headless first: the chromedp-driven anonymous extractor
// returns *cleaner* metadata than the bridge (real og:title +
// og:description + author from LD+JSON, vs the bridge's chrome-y
// "(7) Post | LinkedIn" page title), gets the same body, and
// works without a daemon + extension setup. Operators who need
// the comment thread set
// SOCIAL_FETCH_CHAIN_LINKEDIN=bridge,headless,jina — bridge stays
// in the chain as the auth-capable fallback for those callers.
// Jina last as the remote-service catch-all when neither local
// path is available.
var defaultChain = []fetchchain.Method{
	fetchchain.MethodHeadless,
	fetchchain.MethodBridge,
	fetchchain.MethodJina,
}

// supported lists the methods this fetcher knows how to execute.
// Extra entries in the env var get filtered out via fetchchain.Resolve
// so an operator's typo doesn't accidentally disable the fetcher.
var supported = map[fetchchain.Method]bool{
	fetchchain.MethodBridge:   true,
	fetchchain.MethodJina:     true,
	fetchchain.MethodHeadless: true,
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	if cleaned := stripTracking(raw); cleaned != raw {
		if opts.Audit != nil {
			opts.Audit.Logf("linkedin: stripped tracking params from URL")
		}
		raw = cleaned
	}
	chain := fetchchain.Resolve(fetchchain.FromEnv("linkedin"), defaultChain, supported)
	runners := map[fetchchain.Method]fetchchain.Runner[*core.Item]{
		fetchchain.MethodBridge: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaBridge(ctx, raw, opts)
		},
		fetchchain.MethodJina: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaJina(ctx, raw, opts)
		},
		fetchchain.MethodHeadless: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaHeadless(ctx, raw, opts)
		},
	}
	item, _, err := fetchchain.Run(ctx, "linkedin", raw, opts.Audit, chain, runners)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// fetchViaHeadless drives a fresh stealth Chromium via chromedp and
// runs the resulting HTML through the same cleanHTML + media +
// author extractors as the bridge path. Always anonymous — fetches
// the public guest-preview page LinkedIn serves without auth, which
// already contains the full post body, author, and reaction count
// (just not the comment thread).
//
// Trade-offs vs the other methods:
//
//   - vs bridge: no daemon / no extension needed, but spawns a
//     fresh browser per call (~2s warmup) and can't see comments
//     (auth-walled).
//   - vs jina: local rather than remote (no third-party hop,
//     no rate limit, no JINA_API_KEY needed for paid features),
//     but ~30 MB more memory per fetch and requires Chrome
//     installed on the host.
func (f *Fetcher) fetchViaHeadless(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	res, err := headless.New().Fetch(ctx, raw)
	if err != nil {
		return nil, err
	}
	finalURL := res.FinalURL
	if finalURL == "" {
		finalURL = raw
	}

	// Anonymous-preview HTML has a different DOM tree than the
	// bridge's logged-in output — cleanHTML/trimBoilerplate are
	// tuned for the latter and return empty here. extractHeadless
	// is the matching extractor: walks LD+JSON + og:tags + DOM
	// fallbacks for the guest-preview shape.
	item := extractHeadless(res.HTML, finalURL)
	item.Extra["engine"] = res.Engine

	if opts.Audit != nil {
		opts.Audit.Logf("linkedin: headless path returned %d chars (engine=%s, anonymous — no comments)", len(item.Content), res.Engine)
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
	res, err := renderhtmlmd.NewJinaReader().ReadFull(ctx, raw)
	if err != nil {
		return nil, err
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		return nil, fmt.Errorf("empty markdown from Jina for %s", raw)
	}

	// LinkedIn's page title reads "<headline> | <Author Name> posted
	// on the topic | LinkedIn" — pull the author from it, then trim
	// the chrome off the title so the visible field is the post's
	// first words rather than the LinkedIn boilerplate suffix.
	title := res.Title
	author := parseAuthorFromTitle(title)
	if author != "" {
		if i := strings.Index(title, " | "); i > 0 {
			title = strings.TrimSpace(title[:i])
		}
	}

	if opts.Audit != nil {
		opts.Audit.Logf("linkedin: jina path returned %d chars (no comments — anonymous mode)", len(res.Content))
	}

	return &core.Item{
		Source:      "linkedin",
		Kind:        kindFor(raw),
		URL:         raw,
		CanonicalID: canonicalID(raw),
		Title:       title,
		Author:      author,
		Content:     res.Content,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"via":           "jina",
			"comment_count": 0,
			"anonymous":     true,
		},
	}, nil
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

// stripTracking removes the query string and fragment from a LinkedIn
// URL. LinkedIn appends utm_*/trackingId/rcm/etc. when posts are
// shared through the UI; the post ID lives in the URL path
// (`activity-12345`) so the query is universally tracking, never
// functional. Stripping it before dispatch keeps Item.URL stable
// across re-shares and ledger dedup honest (so the same post fetched
// via two different shares hashes to one ledger row).
//
// Fail-soft: malformed URLs pass through unchanged — the chain will
// surface its own parse error if the URL is genuinely broken.
func stripTracking(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.RawQuery == "" && u.Fragment == "" {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
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
