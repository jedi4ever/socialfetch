package urlutil

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Trivial variants we DO collapse.
		{"lowercase host", "https://EXAMPLE.com/foo", "https://example.com/foo"},
		{"lowercase scheme", "HTTPS://example.com/foo", "https://example.com/foo"},
		{"drop fragment", "https://example.com/foo#bar", "https://example.com/foo"},
		{"trim trailing slash", "https://example.com/foo/", "https://example.com/foo"},
		{"keep root slash", "https://example.com/", "https://example.com/"},
		{"all four together", "HTTPS://EXAMPLE.com/Foo/#anchor", "https://example.com/Foo"},

		// Things we DELIBERATELY don't touch — these are
		// semantics-bearing and changing them would break real
		// fetchers (HackerNews uses ?id=N as the canonical URL).
		{"preserve hn querystring", "https://news.ycombinator.com/item?id=1", "https://news.ycombinator.com/item?id=1"},
		{"preserve param order", "https://x.com/?b=2&a=1", "https://x.com/?b=2&a=1"},
		{"preserve utm trackers", "https://example.com/post?utm_source=x", "https://example.com/post?utm_source=x"},
		{"preserve path case", "https://github.com/Foo/Bar", "https://github.com/Foo/Bar"},
		{"empty fragment only", "https://example.com/foo#", "https://example.com/foo"},

		// Pass-throughs for unparseable input so callers don't
		// need to pre-validate.
		{"bare id", "abc123", "abc123"},
		{"empty", "", ""},
		{"scheme-relative", "//example.com/foo", "//example.com/foo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Normalize(c.in)
			if got != c.want {
				t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestEqual(t *testing.T) {
	if !Equal("https://example.com/foo#a", "HTTPS://example.com/foo/") {
		t.Error("Equal should ignore fragment + case + trailing slash")
	}
	if Equal("https://news.ycombinator.com/item?id=1", "https://news.ycombinator.com/item?id=2") {
		t.Error("Equal must NOT ignore query string differences")
	}
}
