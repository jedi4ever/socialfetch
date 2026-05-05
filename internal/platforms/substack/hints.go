package substack

import _ "embed"

// Hints is the embedded markdown describing Substack-specific
// quirks — paywall, member-only content, custom-domain routing.
// Surfaced via `social-fetch hints substack` and the
// social_fetch_hints MCP tool.
//
//go:embed hints.md
var Hints string
