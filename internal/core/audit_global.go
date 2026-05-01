package core

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultAuditPath returns the path of the global audit log every
// socialfetch invocation appends to. Lives under the user cache dir
// so it doesn't co-exist with source files; falls back to /tmp if the
// cache lookup fails.
//
// Override with SOCIALFETCH_AUDIT_PATH; opt out entirely with
// SOCIALFETCH_AUDIT=0.
func DefaultAuditPath() string {
	if p := os.Getenv("SOCIALFETCH_AUDIT_PATH"); p != "" {
		return p
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "socialfetch", "audit.jsonl")
	}
	return filepath.Join(os.TempDir(), "socialfetch", "audit.jsonl")
}

// AuditEnabled returns false when SOCIALFETCH_AUDIT=0 is set; any
// other value (or unset) enables the global audit log.
func AuditEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SOCIALFETCH_AUDIT"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// auditMaxBytes triggers rotation. 50 MiB keeps the live file
// reasonably tail-able while accumulating enough history for an
// average session. One rotated copy is kept (`audit.jsonl.1`); older
// rotations are discarded.
const auditMaxBytes = 50 * 1024 * 1024

// OpenGlobalAudit returns a writer that emits one JSONL line per call
// to Write to the default audit file, plus a close function.
//
// Each line carries a timestamp, the calling process's PID, the
// subcommand name (fetch / search / timeline / ...) and the message.
// Writes are POSIX-atomic up to PIPE_BUF (typically 4 KiB) so multiple
// concurrent invocations interleave cleanly without explicit locking.
//
// If the file exceeds auditMaxBytes on open, it's rotated to .1 and a
// fresh file starts. Rotation is best-effort: a concurrent process
// rotating at the same time may briefly write to the renamed file —
// acceptable for an audit trail (no events lost, just split).
//
// When the global audit is disabled (SOCIALFETCH_AUDIT=0), returns a
// discard writer and a no-op closer.
func OpenGlobalAudit(cmd string) (io.Writer, func(), error) {
	if !AuditEnabled() {
		return io.Discard, func() {}, nil
	}
	path := DefaultAuditPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return io.Discard, func() {}, fmt.Errorf("audit: mkdir: %w", err)
	}
	if info, err := os.Stat(path); err == nil && info.Size() > auditMaxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return io.Discard, func() {}, fmt.Errorf("audit: open: %w", err)
	}
	return &auditJSONLWriter{f: f, cmd: cmd, pid: os.Getpid()}, func() { _ = f.Close() }, nil
}

// auditJSONLWriter wraps each Write into one JSONL line:
//
//	{"ts":"2026-05-01T18:00:00.123Z","pid":12345,"cmd":"fetch","msg":"…"}
//
// The Write payload is the human-readable message text from
// AuditLogger.Logf — already-formatted, single-line.
type auditJSONLWriter struct {
	f   *os.File
	cmd string
	pid int
	mu  sync.Mutex
}

func (w *auditJSONLWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\r\n")
	line, err := json.Marshal(struct {
		Ts  string `json:"ts"`
		Pid int    `json:"pid"`
		Cmd string `json:"cmd"`
		Msg string `json:"msg"`
	}{
		Ts:  time.Now().UTC().Format(time.RFC3339Nano),
		Pid: w.pid,
		Cmd: w.cmd,
		Msg: msg,
	})
	if err != nil {
		return 0, err
	}
	line = append(line, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(line); err != nil {
		return 0, err
	}
	return len(p), nil
}
