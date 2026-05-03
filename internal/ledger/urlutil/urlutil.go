// Package urlutil collects the URL-normalization helpers shared by
// social-fetch and social-ledger. Single source of truth so the
// ingest path and the lookup path agree on what counts as "the
// same URL", and so a future change (e.g. dropping utm_* tracker
// params) lands in one file.
//
// We deliberately keep normalization *conservative*: only flatten
// surface variations that don't change semantics (lowercase scheme/
// host, strip fragment, trim trailing slash on non-root paths).
// Aggressive transforms — query-param reordering, tracker-param
// stripping, redirect-following — are out of scope here. Those are
// per-source decisions; this package exists to handle the dumb
// stuff every consumer needs.
package urlutil

import (
	"net/url"
	"strings"
)

// Normalize flattens trivial URL variants so two URLs that differ
// only in surface form match. Returns the input unchanged when
// it isn't a parseable absolute URL — callers don't have to
// special-case "could be a bare ID, could be a URL".
//
// What it does:
//   - lowercase scheme and host (URLs are case-insensitive there;
//     case is preserved in path + query)
//   - drop fragment (#anchor — server doesn't see it anyway)
//   - trim trailing slash on non-root paths (/foo/ → /foo;
//     leaves "/" and bare-host alone)
//
// What it explicitly does NOT do:
//   - reorder or canonicalise query params (semantics-bearing)
//   - strip utm_* / fbclid / gclid (per-source decision)
//   - follow redirects (network call, lives elsewhere)
//   - punycode conversion (rare in practice; libs disagree)
func Normalize(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String()
}

// Equal reports whether two URLs normalize to the same string.
// Convenience wrapper; saves callers the two-Normalize boilerplate
// at every call site that's deciding "are these the same URL".
func Equal(a, b string) bool {
	return Normalize(a) == Normalize(b)
}
