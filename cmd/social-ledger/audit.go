package main

// Standalone audit log for social-ledger — symmetric with the
// social-fetch audit log over at internal/core/audit_global.go.
// Records one JSONL line per CLI invocation (subcommand, args
// summary, outcome, duration). Useful for answering:
//
//   "what got recorded into the ledger today, and by whom?"
//   "did that flaky agent loop actually run a forget?"
//   "how often does auto-ingest from social-fetch hit our DB?"
//
// Path: $XDG_CACHE_HOME/social-ledger/audit.jsonl (or
// ~/Library/Caches/social-ledger/audit.jsonl on macOS).
//
// Env vars:
//   SOCIAL_LEDGER_AUDIT      0 → disable, "" / 1 → enable (default)
//   SOCIAL_LEDGER_AUDIT_PATH explicit override of the file path
//
// The audit log never fails the parent invocation: open errors,
// write errors, locked filesystem — all swallowed silently. The
// goal is "best-effort observability", not durability.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	auditEnabledEnv = "SOCIAL_LEDGER_AUDIT"
	auditPathEnv    = "SOCIAL_LEDGER_AUDIT_PATH"
)

// auditEnabled reports whether the audit log should be written
// for this invocation. Truthy / empty / unset = enabled (default
// on). Only the literal "0" / "false" / "no" / "off" disables.
func auditEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(auditEnabledEnv)))
	switch v {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// auditPath returns the absolute path the audit JSONL is written
// to. Empty string means audit is unwritable (disabled or path
// resolution failed); callers should treat as "skip".
func auditPath() string {
	if !auditEnabled() {
		return ""
	}
	if explicit := strings.TrimSpace(os.Getenv(auditPathEnv)); explicit != "" {
		return explicit
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "social-ledger", "audit.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "social-ledger", "audit.jsonl")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", "social-ledger", "audit.jsonl")
	}
	return filepath.Join(home, ".cache", "social-ledger", "audit.jsonl")
}

// auditEntry mirrors the shape social-fetch uses so a tail-er
// (`tail -f` + jq) sees a uniform format across both binaries.
type auditEntry struct {
	TS         string `json:"ts"`
	PID        int    `json:"pid"`
	Cmd        string `json:"cmd"`            // subcommand name
	Args       string `json:"args,omitempty"` // joined positional args, truncated at 200 chars
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"` // first line of error.Error() if non-nil
}

// writeAuditLine appends a single audit JSONL line for the given
// subcommand. Best-effort: any open / encode / write failure is
// swallowed so the parent CLI never errors out because of audit
// problems.
func writeAuditLine(cmd string, args []string, start time.Time, runErr error) {
	path := auditPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	argsLine := strings.Join(args, " ")
	if len(argsLine) > 200 {
		argsLine = argsLine[:197] + "..."
	}
	exit := 0
	errMsg := ""
	if runErr != nil {
		exit = 1
		// Trim to the first line so multi-line errors don't
		// blow up the JSONL row.
		errMsg = strings.SplitN(runErr.Error(), "\n", 2)[0]
	}
	entry := auditEntry{
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		PID:        os.Getpid(),
		Cmd:        cmd,
		Args:       argsLine,
		ExitCode:   exit,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      errMsg,
	}
	enc := json.NewEncoder(f)
	_ = enc.Encode(entry)
}
