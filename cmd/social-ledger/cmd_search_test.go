package main

import "testing"

func TestSanitizeFTS5Query(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Plain barewords pass through unchanged.
		{"single bareword", "harness", "harness"},
		{"two barewords", "harness engineering", "harness engineering"},
		{"prefix asterisk", "harness*", "harness*"},

		// Operators stay bare; lowercase gets uppercased so
		// FTS5 unambiguously treats them as keywords.
		{"OR with barewords", "harness or context", "harness OR context"},
		{"explicit AND", "agent AND skills", "agent AND skills"},
		{"NOT", "agent NOT framework", "agent NOT framework"},
		{"NEAR", "harness NEAR engineering", "harness NEAR engineering"},

		// The original bug — hyphenated terms get phrase-quoted.
		{"hyphenated bareword", "context-engineering", `"context-engineering"`},
		{"the audit-log error case",
			"harness OR context-engineering OR 12-factor",
			`harness OR "context-engineering" OR "12-factor"`},

		// Other reserved chars trigger quoting too.
		{"colon", "url:https", `"url:https"`},
		{"dot in domain", "vercel.com", `"vercel.com"`},
		{"slash in path", "blog/agent-skills", `"blog/agent-skills"`},

		// Already-quoted phrases pass through verbatim.
		{"quoted phrase",
			`"vercel ai sdk"`,
			`"vercel ai sdk"`},
		{"mixed quoted + bareword",
			`"vercel ai" OR firecrawl`,
			`"vercel ai" OR firecrawl`},

		// Parens pass through.
		{"grouped",
			"(harness OR context) AND agent",
			"(harness OR context) AND agent"},

		// Prefix on a hyphenated term: quote word, keep star outside.
		{"hyphen prefix", "harness-eng*", `"harness-eng"*`},

		// Whitespace preserved.
		{"tabs and spaces",
			"harness  OR\tcontext-engineering",
			"harness  OR\t\"context-engineering\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeFTS5Query(c.in)
			if got != c.want {
				t.Errorf("sanitizeFTS5Query(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
