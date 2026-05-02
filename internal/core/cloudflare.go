// Cloudflare-challenge detection.
//
// When Cloudflare's bot management mitigates a request, the response
// is technically valid HTTP — usually a 403 or 503 — but the body is
// a JavaScript challenge ("Just a moment...") instead of the page
// the user wanted. Generic non-2xx handling treats this as "site
// returned an error" and gives up; richer fetchers want to route
// around it (browser bridge for the user's logged-in session, Jina
// for service-backed bypass).
//
// The signals below are stable enough that pattern-matching is safe.
// Cloudflare's `cf-mitigated: challenge` header is the most reliable
// — it's set explicitly when a challenge is served. The other
// signals catch Cloudflare's older challenge layouts and edge cases
// where headers are stripped by intermediate proxies.
package core

import (
	"bytes"
	"net/http"
	"strings"
)

// IsCloudflareBlocked reports whether resp + bodySnippet look like a
// Cloudflare bot-management challenge. resp must not be nil; the
// body snippet should be the first ~512 bytes of the response body
// (more isn't useful — every CF challenge tag we look for sits in
// the head/early body).
//
// The function is conservative: a real 403 or 503 from a non-CF
// origin returns false even if the response body happens to contain
// the word "cloudflare". We require a CF-specific header
// (`cf-mitigated`, `cf-ray`, or `server: cloudflare`) AND a
// challenge-shape body, so a Cloudflare-fronted site that returns a
// real 403 from the origin (auth required, etc.) doesn't get
// misrouted through the bridge.
func IsCloudflareBlocked(resp *http.Response, bodySnippet []byte) bool {
	if resp == nil {
		return false
	}
	// We only treat 403/503 as CF challenges. 200 means content reached
	// us (CF can front a successful response), 401 is auth, etc. — none
	// of those should be retried via bridge/Jina.
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	// `cf-mitigated: challenge` is unambiguous — Cloudflare itself
	// telling us a challenge was served.
	if strings.EqualFold(resp.Header.Get("cf-mitigated"), "challenge") {
		return true
	}
	// Without cf-mitigated, require both a CF header AND
	// challenge-shaped body.
	hasCFHeader := resp.Header.Get("cf-ray") != "" ||
		strings.EqualFold(resp.Header.Get("server"), "cloudflare")
	if !hasCFHeader {
		return false
	}
	body := bytes.ToLower(bodySnippet)
	for _, marker := range cfChallengeMarkers {
		if bytes.Contains(body, marker) {
			return true
		}
	}
	return false
}

// cfChallengeMarkers are byte sequences that show up in Cloudflare
// challenge bodies. Lowercase since IsCloudflareBlocked lowercases
// the snippet before matching. Order doesn't matter — first match
// wins.
var cfChallengeMarkers = [][]byte{
	[]byte("just a moment"),    // visible heading on the challenge page
	[]byte("__cf_bm"),          // the bot-management cookie set by JS
	[]byte("cf-error-details"), // <div> wrapper on classic CF error pages
	[]byte("checking if the site connection is secure"),
	[]byte("__cf_chl_"),                  // challenge token prefix
	[]byte("cdn-cgi/challenge-platform"), // script src on every challenge page
}
