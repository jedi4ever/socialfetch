package arxiv

import "testing"

func TestBuildArxivQuery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Single bare term — wrap in all:.
		{"single term", "transformer", "all:transformer"},

		// Multi-word: AND each, all wrapped in all:. The bug we're
		// fixing — without this, arXiv parses `harness engineering`
		// as `all:harness OR all:engineering`, which gives newest
		// papers about harnesses OR engineering, dominated by
		// engineering's volume.
		{"two words", "harness engineering", "all:harness AND all:engineering"},
		{"three words", "agentic harness engineering",
			"all:agentic AND all:harness AND all:engineering"},

		// Hyphenated terms become phrase-quoted so arXiv doesn't
		// split on the hyphen.
		{"hyphenated", "harness-engineering", `all:"harness-engineering"`},
		{"mixed", "agentic harness-engineering",
			`all:agentic AND all:"harness-engineering"`},

		// Field-prefixed queries pass through.
		{"ti prefix", "ti:transformer", "ti:transformer"},
		{"all prefix", "all:rag", "all:rag"},
		{"category", "cat:cs.LG", "cat:cs.LG"},
		{"author", "au:hinton", "au:hinton"},

		// Explicit boolean operators pass through.
		{"explicit AND", "harness AND engineering", "harness AND engineering"},
		{"explicit OR", "rag OR retrieval", "rag OR retrieval"},
		{"explicit NOT", "transformer NOT vision", "transformer NOT vision"},

		// Parens / phrase quotes — power-user DSL, pass through.
		{"grouped", "(harness AND engineering) OR rag",
			"(harness AND engineering) OR rag"},
		{"phrase", `"large language models"`, `"large language models"`},

		// Edge cases.
		{"empty", "", ""},
		{"whitespace-only single",
			"  transformer  ", "all:  transformer  "}, // looksLikeArxivAdvancedQuery sees no signals; trim was earlier
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildArxivQuery(c.in)
			// Special case: " transformer " — Fields splits to
			// single ["transformer"] so the actual rewrite is
			// "all:transformer". Adjust the test's want.
			if c.name == "whitespace-only single" {
				if got != "all:transformer" {
					t.Errorf("want all:transformer, got %q", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("buildArxivQuery(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
