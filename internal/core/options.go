package core

import (
	"io"
	"log"
	"os"
)

// Options shape every fetch. Sources should honor the fields that apply
// (IncludeComments for HN/Reddit; the rest may ignore them).
type Options struct {
	// IncludeComments controls whether comment trees are fetched. Defaults
	// to true. Set false when you only need the post body.
	IncludeComments bool

	// MaxComments caps the total number of comments returned per item.
	// Zero means "no limit"; sources may still apply their own depth caps.
	MaxComments int

	// GenericExtraction forces the generic article extractor even when a
	// host-specific one (Medium, Substack, ...) would normally claim the
	// URL. Default false = use per-host extractors when available.
	GenericExtraction bool

	// Audit, if non-nil, receives one line per network event: requests,
	// status codes, redirects, errors. Use NewAuditLogger to wire one up.
	Audit *AuditLogger
}

// DefaultOptions returns the options used when callers don't supply any.
func DefaultOptions() Options {
	return Options{IncludeComments: true}
}

// AuditLogger writes one line per significant network event so users (and
// agents) can trace what the fetcher did. It is safe for concurrent use
// because *log.Logger is.
type AuditLogger struct {
	l *log.Logger
}

// NewAuditLogger returns a logger that writes timestamped lines to w. Pass
// nil to silence output.
func NewAuditLogger(w io.Writer) *AuditLogger {
	if w == nil {
		w = io.Discard
	}
	return &AuditLogger{l: log.New(w, "", log.LstdFlags|log.LUTC)}
}

// Stderr is a convenience: logs to os.Stderr.
func StderrAudit() *AuditLogger { return NewAuditLogger(os.Stderr) }

// Logf records a single audit event. Safe to call on a nil receiver.
func (a *AuditLogger) Logf(format string, args ...any) {
	if a == nil {
		return
	}
	a.l.Printf(format, args...)
}
