package main

// Unit tests for the screenshot CLI helpers — slug derivation +
// viewport parsing. End-to-end tests that drive the real binary
// live in cmd/social-fetch/integration_test.go under the
// `integration` build tag (they need a running headless daemon
// or a fresh Chromium spawn).

import (
	"strings"
	"testing"
)

func TestDefaultScreenshotPath(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantSlugs []string // substrings the path should contain
	}{
		{"hostname only", "https://example.com", []string{"social-fetch-screenshot-", "example-com-"}},
		{"strips www", "https://www.example.com/foo", []string{"example-com-foo"}},
		{"deep path uses first segment only", "https://news.ycombinator.com/item?id=1", []string{"news-ycombinator-com-item"}},
		{"non-alnum collapses to dash", "https://example.com/foo+bar%20baz", []string{"example-com-foo"}},
		{"unparseable host falls back to 'page'", "://broken-url", []string{"social-fetch-screenshot-page-"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultScreenshotPath(tc.url)
			for _, want := range tc.wantSlugs {
				if !strings.Contains(got, want) {
					t.Errorf("defaultScreenshotPath(%q) = %q, missing substring %q", tc.url, got, want)
				}
			}
			if !strings.HasSuffix(got, ".png") {
				t.Errorf("expected .png suffix, got %q", got)
			}
			// Ensure social-fetch- prefix on the basename so
			// social_fetch_read_file's safeTempPath accepts it.
			parts := strings.Split(got, "/")
			base := parts[len(parts)-1]
			if !strings.HasPrefix(base, "social-fetch-") {
				t.Errorf("basename %q must start with social-fetch-", base)
			}
		})
	}
}

func TestParseViewport(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		w, h, err := parseViewport("1920x1080")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if w != 1920 || h != 1080 {
			t.Errorf("want 1920x1080, got %dx%d", w, h)
		}
	})
	t.Run("trims whitespace", func(t *testing.T) {
		w, h, err := parseViewport("  1280 x 720  ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if w != 1280 || h != 720 {
			t.Errorf("want 1280x720, got %dx%d", w, h)
		}
	})
	t.Run("rejects malformed", func(t *testing.T) {
		bad := []string{
			"",
			"1920",
			"1920,1080",
			"1920x",
			"x1080",
			"abc x def",
		}
		for _, in := range bad {
			if _, _, err := parseViewport(in); err == nil {
				t.Errorf("parseViewport(%q) should have errored", in)
			}
		}
	})
	t.Run("rejects out-of-range", func(t *testing.T) {
		bad := []string{
			"100x100",   // too small
			"4000x4000", // too big
			"320x100",   // height too small
			"3840x3000", // height too big
		}
		for _, in := range bad {
			if _, _, err := parseViewport(in); err == nil {
				t.Errorf("parseViewport(%q) should have errored on range", in)
			}
		}
	})
	t.Run("accepts edge of range", func(t *testing.T) {
		ok := []string{"320x240", "3840x2160"}
		for _, in := range ok {
			if _, _, err := parseViewport(in); err != nil {
				t.Errorf("parseViewport(%q) should have accepted: %v", in, err)
			}
		}
	})
}
