package core

import (
	"fmt"
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
//
// In addition to the user-facing destination (typically stderr or a file
// passed via -l), the logger optionally tees to a global JSONL writer
// shared across every socialfetch invocation — see audit_global.go and
// the `monitor` subcommand. The two destinations are independent so
// the user's chosen verbosity doesn't constrain the audit trail.
type AuditLogger struct {
	l      *log.Logger
	global io.Writer
}

// NewAuditLogger returns a logger that writes timestamped lines to w. Pass
// nil to silence output.
func NewAuditLogger(w io.Writer) *AuditLogger {
	if w == nil {
		w = io.Discard
	}
	return &AuditLogger{l: log.New(w, "", log.LstdFlags|log.LUTC)}
}

// AttachGlobal sets the JSONL audit sink each Logf call also writes to.
// Pass nil to detach. The sink is expected to wrap each Write into a
// JSONL line; see auditJSONLWriter in audit_global.go.
func (a *AuditLogger) AttachGlobal(w io.Writer) {
	if a == nil {
		return
	}
	a.global = w
}

// Stderr is a convenience: logs to os.Stderr.
func StderrAudit() *AuditLogger { return NewAuditLogger(os.Stderr) }

// Logf records a single audit event. Safe to call on a nil receiver.
func (a *AuditLogger) Logf(format string, args ...any) {
	if a == nil {
		return
	}
	a.l.Printf(format, args...)
	if a.global != nil {
		// Global writer expects single-line message text; the JSONL
		// wrapper adds ts/pid/cmd around it.
		_, _ = fmt.Fprintf(a.global, format, args...)
	}
}
