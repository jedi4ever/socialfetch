package mcp

// flexint_test.go locks in the JSON-binding behaviour of the
// flexInt type used across MCP arg structs. The point of flexInt
// is to accept both `3` and `"3"` from the wire, since some MCP
// clients (Claude Code's stdio path under specific call shapes)
// quote numeric arguments even when the schema says they're
// numbers. If a future refactor changes int-typed fields back to
// plain int, this test won't catch it directly — but it does
// catch any regression in flexInt's tolerant unmarshal.

import (
	"encoding/json"
	"testing"
)

func TestFlexInt_UnmarshalsJSONNumbersAndStrings(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		want   int
		errors bool
	}{
		{"plain number", `3`, 3, false},
		{"large number", `1048576`, 1048576, false},
		{"zero", `0`, 0, false},
		{"negative", `-5`, -5, false},
		{"quoted string number", `"3"`, 3, false},
		{"quoted negative", `"-7"`, -7, false},
		{"empty string", `""`, 0, false},
		{"explicit null", `null`, 0, false},
		{"float with zero fraction", `3.0`, 3, false},
		{"quoted float zero fraction", `"3.0"`, 3, false},
		{"float with non-zero fraction → error", `3.5`, 0, true},
		{"alphabetic string → error", `"abc"`, 0, true},
		{"object → error", `{}`, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f flexInt
			err := json.Unmarshal([]byte(c.input), &f)
			if c.errors {
				if err == nil {
					t.Errorf("input %s: expected error, got value %d", c.input, int(f))
				}
				return
			}
			if err != nil {
				t.Fatalf("input %s: unexpected error: %v", c.input, err)
			}
			if int(f) != c.want {
				t.Errorf("input %s: got %d, want %d", c.input, int(f), c.want)
			}
		})
	}
}

func TestFlexInt_OmittedFieldStaysZero(t *testing.T) {
	// When a JSON object omits a flexInt field entirely, the
	// surrounding struct's omitempty + Go's zero-value semantics
	// should keep it at 0 — no unmarshal call happens. This test
	// catches accidental regressions where flexInt becomes
	// non-Unmarshaler-safe (e.g. someone makes UnmarshalJSON
	// require non-empty input).
	type s struct {
		A flexInt `json:"a,omitempty"`
		B flexInt `json:"b,omitempty"`
	}
	var got s
	if err := json.Unmarshal([]byte(`{"a":3}`), &got); err != nil {
		t.Fatal(err)
	}
	if int(got.A) != 3 {
		t.Errorf("got.A = %d, want 3", int(got.A))
	}
	if int(got.B) != 0 {
		t.Errorf("got.B = %d, want 0 (omitted field)", int(got.B))
	}
}
