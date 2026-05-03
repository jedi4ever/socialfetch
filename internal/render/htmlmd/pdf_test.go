package htmlmd

import "testing"

func TestIsPDFURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://arxiv.org/pdf/1706.03762", false}, // no extension
		{"https://arxiv.org/pdf/1706.03762.pdf", true},
		{"https://example.com/doc.pdf", true},
		{"https://example.com/doc.PDF", true},
		{"https://example.com/doc.pdf?download=1", true},
		{"https://example.com/doc.pdf#page=3", true},
		{"https://example.com/doc.html", false},
		{"https://example.com/", false},
		{"https://example.com/path/to/something", false},
		{"not a url", false},
	}
	for _, c := range cases {
		got := IsPDFURL(c.url)
		if got != c.want {
			t.Errorf("IsPDFURL(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestIsPDFContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"application/pdf", true},
		{"application/pdf; charset=binary", true},
		{"Application/PDF", true},
		{"application/x-pdf", true},
		{"text/html", false},
		{"text/html; charset=utf-8", false},
		{"", false},
	}
	for _, c := range cases {
		got := IsPDFContentType(c.ct)
		if got != c.want {
			t.Errorf("IsPDFContentType(%q) = %v, want %v", c.ct, got, c.want)
		}
	}
}

func TestPickPDFReader(t *testing.T) {
	// Default → JinaReader
	r := pickPDFReader("")
	if r == nil {
		t.Error("default should not be nil")
	}
	if _, ok := r.(*JinaReader); !ok {
		t.Errorf("default should be *JinaReader, got %T", r)
	}
	// Explicit jina
	if _, ok := pickPDFReader("jina").(*JinaReader); !ok {
		t.Error("jina should be *JinaReader")
	}
	// Off → nil
	if r := pickPDFReader("off"); r != nil {
		t.Errorf("off should be nil, got %T", r)
	}
	if r := pickPDFReader("none"); r != nil {
		t.Errorf("none should be nil, got %T", r)
	}
	// Unknown → fall back to default (JinaReader, not nil)
	if _, ok := pickPDFReader("frobnicator").(*JinaReader); !ok {
		t.Error("unknown value should fall back to JinaReader")
	}
}
