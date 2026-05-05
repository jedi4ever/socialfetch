package mcp

// Unit tests for the MCP-side argument parsers. The end-to-end
// behaviour (Add → Subscribe → Unsubscribe → Remove via stdio MCP)
// is covered by cmd/social-fetch/integration_test.go under the
// `integration` build tag — that one drives the real binary.
// This file just pins the small validation surface that lives in
// the MCP layer itself: parseSocialPairs.

import (
	"reflect"
	"testing"
)

func TestParseSocialPairs(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		got, err := parseSocialPairs(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("want nil, got %v", got)
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		got, err := parseSocialPairs([]string{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("want nil, got %v", got)
		}
	})

	t.Run("simple valid pairs", func(t *testing.T) {
		got, err := parseSocialPairs([]string{
			"linkedin=jane-doe",
			"x=jane",
			"github=jane",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]string{
			"linkedin": "jane-doe",
			"x":        "jane",
			"github":   "jane",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("want %v, got %v", want, got)
		}
	})

	t.Run("trims whitespace + lowercases keys", func(t *testing.T) {
		got, err := parseSocialPairs([]string{"  LinkedIn  =  jane  "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]string{"linkedin": "jane"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("want %v, got %v", want, got)
		}
	})

	t.Run("skips blank entries", func(t *testing.T) {
		got, err := parseSocialPairs([]string{"x=jane", "", "   "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]string{"x": "jane"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("want %v, got %v", want, got)
		}
	})

	t.Run("rejects malformed entries", func(t *testing.T) {
		bad := []string{
			"no-equals-sign",
			"=value-only",
			"key-only=",
		}
		for _, in := range bad {
			if _, err := parseSocialPairs([]string{in}); err == nil {
				t.Errorf("parseSocialPairs([%q]) should have errored", in)
			}
		}
	})

	t.Run("preserves URL values with embedded =", func(t *testing.T) {
		// website handles can be URLs that look like
		// "website=https://example.com/?ref=jane" — only the FIRST
		// `=` should split; the rest stays in the value.
		got, err := parseSocialPairs([]string{
			"website=https://example.com/?ref=jane",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]string{
			"website": "https://example.com/?ref=jane",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("want %v, got %v", want, got)
		}
	})
}
