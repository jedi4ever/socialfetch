package htmlmd

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
)

// PDF support — separate from Reader because the trigger is different
// (URL-extension or content-type, not a global "use service" flag) but
// the underlying transport is the same r.jina.ai endpoint.
//
// Why route PDFs through a service: pure-Go PDF text extractors
// (ledongthuc/pdf etc.) are mediocre on the multi-column layouts
// typical of academic papers — random text ordering, dropped
// equations, fragmented sentences. Native poppler (pdftotext) is
// excellent but adds an external dependency. Jina Reader handles
// PDFs the same way it handles HTML: send `https://r.jina.ai/<pdf>`,
// get back clean markdown. Zero binary bloat, no cgo, no install
// required.
//
// `PDF_READER` env var picks the implementation:
//
//	jina (default) — JinaReader, same instance HTML2MD_READER uses
//	off / none     — return nil; caller surfaces a clear error
//
// Future values like `pdftotext` (shell out to poppler) can drop in
// as additional cases without touching call sites.

// IsPDFURL recognises PDF URLs by extension. Used as a cheap
// pre-fetch hint so the article fetcher can pick the PDF path
// without paying for a HEAD request first. Doesn't catch every PDF
// (some servers serve PDFs at extension-less URLs) — for those,
// IsPDFContentType after fetch is the reliable signal.
func IsPDFURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.HasSuffix(p, ".pdf")
}

// IsPDFContentType is the reliable post-fetch signal — set when the
// server's Content-Type starts with `application/pdf` (the IANA
// registration; Cloudflare and some misconfigured servers emit
// `application/x-pdf` too).
func IsPDFContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return strings.HasPrefix(ct, "application/pdf") ||
		strings.HasPrefix(ct, "application/x-pdf")
}

// PDFReader is the URL→markdown converter for PDF documents.
// Implementations must respect ctx and be safe for concurrent use.
// Same shape as Reader — the two stay separate so a caller can pick
// "service for HTML" + "local for PDF" or vice versa without one
// switch dictating both.
type PDFReader interface {
	Read(ctx context.Context, url string) (markdown string, err error)
}

// DefaultPDFReader returns the configured PDF reader. nil means "no
// PDF support is configured" — the caller should surface a clear
// error pointing the user at PDF_READER and document the install
// requirements for any future shell-based readers.
//
// Cached on first call.
func DefaultPDFReader() PDFReader {
	defaultPDFReaderOnce.Do(func() {
		defaultPDFReader = pickPDFReader(os.Getenv("PDF_READER"))
	})
	return defaultPDFReader
}

// pickPDFReader resolves the PDF_READER env var to a concrete reader.
// Empty / unset / "jina" → JinaReader (default). "off" / "none" →
// nil so the caller emits an error rather than silently producing
// empty content. Unknown values follow the same fail-soft rule as
// HTML2MD_READER's pickReader — fall back to default rather than
// breaking builds on a typo.
func pickPDFReader(name string) PDFReader {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "off", "none", "disabled":
		return nil
	case "", "jina", "default":
		return NewJinaReader()
	}
	// Future: case "pdftotext": return NewPdftotextReader()
	return NewJinaReader()
}

var (
	defaultPDFReaderOnce sync.Once
	defaultPDFReader     PDFReader
)
