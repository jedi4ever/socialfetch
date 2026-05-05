package medium

import _ "embed"

// Hints is the embedded markdown describing Medium-specific
// quirks — paywall, member-only content, anti-bot degradation.
// Surfaced via `social-fetch hints medium` and the
// social_fetch_hints MCP tool.
//
//go:embed hints.md
var Hints string
