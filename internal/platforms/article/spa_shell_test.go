package article

import (
	"strings"
	"testing"
)

// TestLooksLikeSPAShell exercises the heuristic that decides
// "this 4xx came with a Next.js / Nuxt / Vite scaffolding HTML
// body, NOT a real not-found page" — drives the UA-sniff fallback
// path in directFetch.
//
// The fixture sizes matter: looksLikeSPAShell rejects bodies under
// 1 KB outright (real 404 messages are tiny), so each positive
// fixture pads to >1 KB before adding the marker.
func TestLooksLikeSPAShell(t *testing.T) {
	pad := strings.Repeat("x", 1024)

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			"nextjs marker",
			pad + `<link rel="stylesheet" href="/_next/static/css/abc.css">`,
			true,
		},
		{
			"nextjs data-n-g attr",
			pad + `<link href="/foo.css" data-n-g="">`,
			true,
		},
		{
			"nuxt window block",
			pad + `<script>window.__NUXT__ = {state:{}}</script>`,
			true,
		},
		{
			"generic next root",
			pad + `<div id="__next"></div>`,
			true,
		},
		{
			"generic vue root",
			pad + `<div id="app"></div>`,
			true,
		},
		{
			"plain 404 (no marker, no SPA scaffolding)",
			pad + "<html><body><h1>Not Found</h1></body></html>",
			false,
		},
		{
			"too small to judge",
			"<html><body>Not Found</body></html>",
			false,
		},
		{
			"empty",
			"",
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := looksLikeSPAShell([]byte(c.body))
			if got != c.want {
				t.Errorf("looksLikeSPAShell = %v, want %v", got, c.want)
			}
		})
	}
}
