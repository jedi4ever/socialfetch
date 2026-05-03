package twitter

import _ "embed"

// Hints is the embedded markdown describing X-specific quirks
// (recent-search 7d cap, consumer-key auth, rate limits, etc.).
// Surfaced via `social-fetch hints x` and the social_fetch_hints
// MCP tool so agents can self-discover platform gotchas without
// scraping SKILL.md or trial-and-erroring against the API.
//
// The `_` blank import on `embed` is required for the //go:embed
// directive to work — go vet flags the file otherwise.
//
//go:embed hints.md
var Hints string
