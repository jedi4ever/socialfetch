package htmlmd

import (
	"context"
	"os"
	"strings"
	"sync"
)

// ServiceFetcher is the interface for service-backed URL→markdown
// fetchers. Unlike Converter (which takes pre-fetched HTML), a
// ServiceFetcher does the fetch itself — useful when the local fetch
// path is blocked (Cloudflare, JS-rendered SPAs) or when the service
// can do readability extraction better than the local pipeline.
//
// Renamed from `Reader` (Nov 2026): the old name suggested a pure
// post-processor like `Converter`, when it's really a fetch
// implementation. The shape (Read method) and behaviour are
// unchanged. `Reader` is kept as a type alias for one release so
// downstream callers don't break.
//
// Implementations must respect the context for cancellation/deadline
// and be safe for concurrent use.
type ServiceFetcher interface {
	Read(ctx context.Context, url string) (markdown string, err error)
}

// Reader is the legacy alias for ServiceFetcher. Kept for one release
// after the rename so external callers compile; new code should use
// ServiceFetcher directly.
//
// Deprecated: use ServiceFetcher.
type Reader = ServiceFetcher

// DefaultReader returns the URL→markdown service fetcher selected by
// the HTML2MD_READER env var. Returns nil for the "local" sentinel —
// callers interpret that as "use the existing fetch + Converter
// pipeline rather than a service-backed fetcher".
//
// Cached on first call.
//
// Note: HTML2MD_READER is being phased out in favour of
// SOCIAL_FETCH_CHAIN_ARTICLE (e.g. `=jina,http,bridge` for the same
// effect). The env var is still honoured for one release; a
// deprecation log line surfaces in the audit trail when it's set.
func DefaultReader() ServiceFetcher {
	defaultReaderOnce.Do(func() {
		defaultReader = pickReader(os.Getenv("HTML2MD_READER"))
	})
	return defaultReader
}

// IsServiceBacked reports whether DefaultReader returned a real
// implementation (vs. nil for "local"). Sugar so callers don't have
// to nil-check.
func IsServiceBacked() bool {
	return DefaultReader() != nil
}

func pickReader(name string) ServiceFetcher {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "jina":
		return NewJinaReader()
	case "", "local", "off", "none":
		return nil
	}
	// Unknown value → behave like "local" rather than failing builds.
	return nil
}

var (
	defaultReaderOnce sync.Once
	defaultReader     ServiceFetcher
)
