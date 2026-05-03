// Package ledger is a thin client for social-ledger that lives
// in the parent social-fetch binary. When SOCIALFETCH_LEDGER=1 is in
// the environment, every successful fetch / timeline / research item
// is auto-piped into `social-ledger ingest` as a subprocess so
// agents don't have to wire up the pipeline themselves.
//
// Design notes:
//
//   - JSONL contract only — we never import the ledger's Go types.
//     The ledger module stays separately liftable to its own repo
//     (jedi4ever/social-skills-ledger) without social-fetch following
//     it. We marshal core.Item with encoding/json and let the
//     ledger's permissive parser map the fields.
//
//   - Subprocess (not in-process) — costs a fork+exec per call
//     (~5-20ms on a warm binary cache) but keeps the boundary clean
//     and means an absent / broken ledger binary degrades gracefully
//     to "exactly the behaviour you had before SOCIALFETCH_LEDGER=1".
//
//   - Best-effort — every failure path writes to the supplied audit
//     logger and returns nil. The parent fetch never fails because
//     the ledger is unhappy. If you want hard guarantees, pipe
//     manually.
//
//   - Buffered when caller has multiple items (research's angle
//     fan-out, multi-URL fetch). One subprocess, N JSONL lines on
//     stdin. The ledger's `ingest` subcommand is built for this.
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// EnabledEnv is the master switch. Anything truthy ("1", "true",
// "yes", case-insensitive) flips the auto-ingest on.
const (
	EnabledEnv = "SOCIALFETCH_LEDGER"
	BinaryEnv  = "SOCIALFETCH_LEDGER_BIN"
	DirEnv     = "SOCIALFETCH_LEDGER_DIR"
)

// Enabled reports whether the auto-ingest hook should fire on this
// invocation. Three states map to SOCIALFETCH_LEDGER:
//
//	1 / true / yes / on    → explicit enable
//	0 / false / no / off   → explicit disable (skip even if a
//	                         binary is on PATH)
//	auto / "" / unset      → auto-detect: enable when binaryPath()
//	                         resolves, otherwise no-op silently
//
// Default is auto so users who install social-ledger get the
// ingest "for free" — and users who haven't installed it never see
// any failure noise. Cached via sync.Once so the auto-detection's
// stat call doesn't repeat on every fetch in a long-running
// process (MCP server, research orchestrator, etc.).
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnabledEnv)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	// Empty / "auto" / unrecognised → auto-detect.
	return autoDetectEnabled()
}

var (
	autoDetectOnce   sync.Once
	autoDetectResult bool
)

func autoDetectEnabled() bool {
	autoDetectOnce.Do(func() {
		_, err := binaryPath()
		autoDetectResult = err == nil
	})
	return autoDetectResult
}

// resetAutoDetectForTests clears the cached auto-detection result
// so tests that mutate $PATH or $SOCIALFETCH_LEDGER_BIN see fresh
// behaviour. Not exported for production use — t.Setenv already
// implies a single-test scope, this just bridges the cache.
func resetAutoDetectForTests() {
	autoDetectOnce = sync.Once{}
	autoDetectResult = false
}

// Ingest pipes the supplied items into `social-ledger ingest`
// as a single JSONL stream. No-op + nil error when Enabled() is
// false or when the ledger binary can't be located. Failures during
// the actual ingest are logged via the audit logger pulled from ctx
// and swallowed — see package doc for rationale.
//
// Pass items by value so the caller can keep using the same slice
// after this returns. Empty / nil slice is a fast no-op.
func Ingest(ctx context.Context, items ...core.Item) {
	if !Enabled() || len(items) == 0 {
		return
	}
	audit := core.AuditFromContext(ctx)

	bin, err := binaryPath()
	if err != nil {
		if audit != nil {
			audit.Logf("ledger: skipping (binary not found: %v)", err)
		}
		return
	}

	// Marshal every item up front — if marshal fails for one item
	// we still want to ingest the rest. The ledger's ingest reads
	// JSONL line-by-line, so we just skip bad lines on our side.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			if audit != nil {
				audit.Logf("ledger: marshal failed for url=%s: %v", it.URL, err)
			}
		}
	}
	if buf.Len() == 0 {
		return
	}

	args := []string{"ingest"}
	// The ledger picks up SOCIALFETCH_LEDGER_DIR from its own env
	// already, but be explicit with --data-dir so a misconfigured
	// child env doesn't silently land items in the wrong store.
	if dir := strings.TrimSpace(os.Getenv(DirEnv)); dir != "" {
		args = append(args, "--data-dir", dir)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = &buf
	// Capture stderr so a broken ledger surfaces in the audit log
	// instead of leaking to the parent's stderr (which the agent
	// or MCP transport may be reading as the JSON-RPC channel).
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	if err := cmd.Run(); err != nil {
		if audit != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			audit.Logf("ledger: ingest failed (%d items): %s", len(items), msg)
		}
		return
	}
	if audit != nil {
		audit.Logf("ledger: ingested %d item(s)", len(items))
	}
}

// IngestSources writes URL+title stub items for citation-shaped data
// (the [].core.Source ask/search return alongside the synthesized
// answer). Each stub goes in under source="citation" with the
// snippet as summary and no body content — agents browsing the
// ledger see "we know this URL exists and what it's about", and
// can later run `social-fetch fetch <url>` to upgrade it to a full
// item under its real source key.
//
// Citation stubs and full fetched items can coexist in the ledger
// for the same URL (different keys: "citation::<url>" vs
// "<actual-source>::<url>"). That's intentional — a stub records
// "we saw this referenced", a full item records "we fetched it".
//
// Skips silently when Enabled() is false. Sources with empty URLs
// are dropped (some providers emit them for anchor-only refs).
func IngestSources(ctx context.Context, sources ...core.Source) {
	if !Enabled() || len(sources) == 0 {
		return
	}
	now := time.Now().UTC()
	stubs := make([]core.Item, 0, len(sources))
	for _, s := range sources {
		if strings.TrimSpace(s.URL) == "" {
			continue
		}
		stubs = append(stubs, core.Item{
			Source:    "citation",
			URL:       s.URL,
			Title:     s.Title,
			Summary:   s.Snippet,
			Published: s.Published,
			FetchedAt: now,
		})
	}
	if len(stubs) == 0 {
		return
	}
	Ingest(ctx, stubs...)
}

// binaryPath returns the absolute path to social-ledger. Lookup
// order:
//
//  1. $SOCIALFETCH_LEDGER_BIN — explicit override
//  2. $PATH lookup via exec.LookPath
//  3. social-ledger as a sibling of the running social-fetch
//     binary — handy during in-tree dev where `make build` and
//     `make ledger-build` both drop into ./dist/.
//
// Errors with a message naming what was tried so the operator can
// fix it without spelunking the source.
func binaryPath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv(BinaryEnv)); explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, nil
		}
		return "", fmt.Errorf("%s=%q does not exist", BinaryEnv, explicit)
	}
	if p, err := exec.LookPath("social-ledger"); err == nil {
		return p, nil
	}
	// Same-dir dev convenience: dist/social-fetch + dist/social-ledger
	// after `make build && make ledger-build`.
	self, err := os.Executable()
	if err == nil {
		guess := filepath.Join(filepath.Dir(self), "social-ledger")
		if _, err := os.Stat(guess); err == nil {
			return guess, nil
		}
	}
	return "", fmt.Errorf("social-ledger not on $PATH (set %s or install via `go install ./cmd/social-ledger`)", BinaryEnv)
}
