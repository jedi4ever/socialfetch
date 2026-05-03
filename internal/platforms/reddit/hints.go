package reddit

import _ "embed"

// Hints is the embedded markdown describing Reddit-specific quirks
// — per-IP rate limit, anonymous search relevance, comment-tree
// size. Surfaced via `social-fetch hints reddit`.
//
//go:embed hints.md
var Hints string
