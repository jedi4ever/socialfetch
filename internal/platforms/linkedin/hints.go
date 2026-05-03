package linkedin

import _ "embed"

// Hints is the embedded markdown describing LinkedIn-specific
// quirks — bridge-required auth, rate-limit risk, no API key shape.
// Surfaced via `social-fetch hints linkedin` and the
// social_fetch_hints MCP tool.
//
//go:embed hints.md
var Hints string
