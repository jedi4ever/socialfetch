// Package provenance maps a ledger entry's `source` column to a
// trust-class string the agent can read. Centralized here because
// both cmd/social-ledger (`seen` text + JSON output) and
// internal/mcp (`social_ledger_get` envelope) need the same
// classification — duplicating it would let them drift.
//
// The convention is:
//
//   - "auto-fetched" — entry was ingested by social_fetch_* / fetch
//     / search / ask / timeline / research / bridge. We pulled the
//     URL ourselves, ran our own extractor, normalised the
//     markdown. High trust.
//
//   - "agent-recorded" — entry was stored via `social-ledger
//     record`, meaning an agent fed in content it got from somewhere
//     else (Claude WebFetch, the research tool, a curl one-off,
//     hand paste). Trust depends on what was fed in.
//
//   - "unknown" — the source column has a value we don't recognise.
//     Reported as-is so the agent can default to caution rather
//     than silently assume "auto-fetched".
package provenance

import "strings"

// Classify returns the trust-class label for a given `source`
// string. Empty source yields "unknown" — caller should treat that
// as "no source recorded, can't classify" rather than silently
// allow auto-fetched-grade trust.
func Classify(source string) string {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		return "unknown"
	}
	switch source {
	case "hackernews", "reddit", "github", "x", "twitter", "linkedin",
		"youtube", "bluesky", "arxiv", "medium", "substack", "rss",
		"article", "atom":
		return "auto-fetched"
	case "webfetch", "manual", "research-tool", "research", "citation":
		return "agent-recorded"
	default:
		return "unknown"
	}
}
