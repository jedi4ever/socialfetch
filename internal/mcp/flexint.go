package mcp

// flexInt is a JSON-binding helper for tool argument fields whose
// MCP descriptor declares them as numbers. Some MCP clients
// (notably Claude Code's stdio transport in certain paths)
// serialize numeric arguments as quoted strings (`"limit":"3"`)
// even when the schema says they're numbers, which would otherwise
// fail mcp-go's typed-handler binding with "cannot unmarshal
// string into Go struct field … of type int".
//
// flexInt's UnmarshalJSON accepts both `3` and `"3"` so the
// handler always sees an int. Empty / missing / "null" values
// yield 0 — matching the zero-value semantics of the original
// `int` fields, so callers that range from 0 to "use the
// default" don't have to special-case anything.

import (
	"fmt"
	"strconv"
	"strings"
)

type flexInt int

// UnmarshalJSON accepts a JSON number or a quoted numeric string.
// Returns nil for empty input so `omitempty` field semantics stay
// intact. Anything else (a non-numeric string, a JSON object,
// floats with non-zero fractional part) errors with a clear
// message so a caller passing `"limit":"abc"` gets actionable
// signal rather than a silent zero.
func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	// Strip JSON-string quotes if present. Don't touch unquoted
	// values — leaving "3.0" unstripped lets ParseFloat catch a
	// fractional value below.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		*f = flexInt(n)
		return nil
	}
	// Tolerate float-shaped values whose fractional part is zero
	// ("3.0" → 3) — common when a JS client does the JSON encode.
	if v, err := strconv.ParseFloat(s, 64); err == nil && v == float64(int64(v)) {
		*f = flexInt(int64(v))
		return nil
	}
	return fmt.Errorf("flexInt: cannot parse %q as integer", s)
}
