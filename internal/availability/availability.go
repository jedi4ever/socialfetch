// Package availability is the central catalog of what each fetch /
// search / ask / timeline provider needs to be functional. Used by
// `social-fetch list` (CLI) and the social_fetch_list_providers MCP
// tool so agents (and humans) can see at a glance which platforms
// are usable in the current environment vs. which would fail with a
// "missing X_API_KEY" error if invoked.
//
// Centralized here rather than spread across each provider so the
// list output, the MCP tool, and any future preflight check stay in
// sync. If a provider's auth shape changes (e.g. X v2 starts
// accepting OAuth2 in addition to v1.1 keys), update this catalog
// alongside the provider's New() — same lockstep rule as SKILL.md.
package availability

import (
	"os"
	"strings"
)

// EnvReqs returns the env-var names a provider needs to be
// functional in the given category. Empty result means "no auth
// required" — the provider works as soon as it's invoked.
//
// Categories: "fetch", "search", "ask", "timeline".
//
// When a provider has alternate auth shapes (e.g. Google ask accepts
// either GEMINI_API_KEY or GOOGLE_API_KEY), each candidate is its own
// inner slice; Status picks the first one that's set.
func EnvReqs(category, name string) [][]string {
	name = strings.ToLower(name)
	switch category {
	case "fetch":
		switch name {
		case "twitter", "x":
			return [][]string{{"X_API_KEY", "X_API_SECRET"}}
		}
	case "search":
		switch name {
		case "google":
			return [][]string{{"GOOGLE_API_KEY", "GOOGLE_CSE_ID"}}
		case "brave":
			return [][]string{{"BRAVE_API_KEY"}}
		case "serpapi":
			return [][]string{{"SERPAPI_KEY"}}
		case "tavily":
			return [][]string{{"TAVILY_API_KEY"}}
		case "perplexity":
			return [][]string{{"PERPLEXITY_API_KEY"}}
		case "twitter", "x":
			return [][]string{{"X_API_KEY", "X_API_SECRET"}}
		case "youtube":
			return [][]string{{"YOUTUBE_API_KEY"}}
		case "bluesky":
			return [][]string{{"BLUESKY_HANDLE", "BLUESKY_APP_PASSWORD"}}
		}
	case "ask":
		switch name {
		case "perplexity":
			return [][]string{{"PERPLEXITY_API_KEY"}}
		case "grok":
			return [][]string{{"XAI_API_KEY"}}
		case "openai":
			return [][]string{{"OPENAI_API_KEY"}}
		case "anthropic":
			return [][]string{{"ANTHROPIC_API_KEY"}}
		case "gemini":
			return [][]string{{"GEMINI_API_KEY"}, {"GOOGLE_API_KEY"}}
		case "tavily":
			return [][]string{{"TAVILY_API_KEY"}}
		case "serpapi":
			return [][]string{{"SERPAPI_KEY"}}
		}
	case "timeline":
		switch name {
		case "x":
			return [][]string{{"X_API_KEY", "X_API_SECRET"}}
		}
	}
	return nil
}

// NeedsBridge reports whether a provider requires the local browser
// bridge. Bridge providers are reported separately from
// missing-API-keys because the bridge is a dynamic service the user
// can start on demand.
func NeedsBridge(category, name string) bool {
	name = strings.ToLower(name)
	switch category {
	case "fetch":
		return name == "linkedin" || name == "medium" || name == "substack"
	case "search":
		return name == "linkedin"
	case "timeline":
		return name == "linkedin"
	}
	return false
}

// Status returns a short label describing the current availability of
// a provider:
//
//   - ""               — fully configured, ready to use
//   - "missing X[ + Y]" — required env vars not set
//   - "needs bridge"    — bridge required (caller may append liveness)
//
// Bridge liveness is left to the caller (CLI `list` does a one-shot
// probe with caching; MCP returns the static "needs bridge" so the
// agent can call social_fetch_bridge_status itself).
func Status(category, name string) string {
	if alts := EnvReqs(category, name); len(alts) > 0 {
		for _, alt := range alts {
			if envsSet(alt) {
				goto authOK
			}
		}
		var missing []string
		for _, e := range alts[0] {
			if strings.TrimSpace(os.Getenv(e)) == "" {
				missing = append(missing, e)
			}
		}
		if len(missing) == 0 {
			return "missing auth"
		}
		return "missing " + strings.Join(missing, " + ")
	}
authOK:
	if NeedsBridge(category, name) {
		return "needs bridge"
	}
	return ""
}

// Available is sugar for `Status(...) == ""` — true when the
// provider is fully configured AND doesn't depend on the bridge.
// Use this when you want a hard "would this provider work right
// now" boolean rather than a human-readable reason.
func Available(category, name string) bool {
	return Status(category, name) == ""
}

func envsSet(envs []string) bool {
	for _, e := range envs {
		if strings.TrimSpace(os.Getenv(e)) == "" {
			return false
		}
	}
	return true
}
