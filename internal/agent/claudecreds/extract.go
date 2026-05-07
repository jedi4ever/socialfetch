// Package claudecreds extracts the operator's Claude Code OAuth
// credentials from the host so they can be forwarded into a
// containerised inner claude (researcher / agent) as
// CLAUDE_OAUTH_CREDENTIALS=<base64>. The container's entrypoint
// (docker-agent-entrypoint.sh) decodes the env var into
// ~/.claude/.credentials.json, which the inner claude reads at
// startup — same auth path as the host claude.
//
// Source order, first hit wins:
//
//  1. macOS Keychain entry "Claude Code-credentials" — read via
//     `security find-generic-password -s "..." -w`. This is
//     where claude-code stores credentials by default on macOS;
//     the operator never has to copy them anywhere.
//  2. ~/.claude/credentials.json — the file fallback. Used on
//     Linux (no native keychain integration in claude-code) and
//     when a macOS user has the file present from an older
//     install.
//
// The returned bytes are the base64 of the credentials JSON,
// suitable for setting as an env var directly. Empty error +
// empty bytes when nothing is found — the caller treats that as
// "no creds available, fall back to ANTHROPIC_API_KEY or
// whatever else was set".
//
// Mirrors ~/dev/dclaude/src/extensions/claude/credentials.sh —
// same source order, same env-var contract — translated to Go
// so callers can integrate the lookup without spawning a shell.
package claudecreds

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// MacOSKeychainService is the service name claude-code writes
// under in the macOS Keychain. Consistent across versions so we
// can hard-code; if it ever changes upstream we'll surface the
// shift as an empty Extract() result and fall through to the
// file path.
const MacOSKeychainService = "Claude Code-credentials"

// CredentialsFilePath returns the conventional location of the
// claude-code credentials JSON when the keychain isn't available
// (Linux, or a user who's prefers the file). Resolves $HOME at
// call time — tests can $HOME-override without touching this
// package.
func CredentialsFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", ".credentials.json")
}

// Extract reads the host's claude-code credentials and returns
// them base64-encoded — ready to drop into a container env as
// CLAUDE_OAUTH_CREDENTIALS=<value>. Returns empty + nil error
// when nothing's available (caller decides whether to warn or
// silently fall through).
//
// Order: macOS Keychain → file fallback. Both produce the same
// JSON shape; we don't decode it, just base64-encode the raw
// bytes the entrypoint will reverse on the inner side.
func Extract() (string, error) {
	if runtime.GOOS == "darwin" {
		if blob, err := readKeychain(); err == nil && len(blob) > 0 {
			return base64.StdEncoding.EncodeToString(blob), nil
		}
		// Fall through to file.
	}
	blob, err := readFile()
	if err != nil {
		return "", err
	}
	if len(blob) == 0 {
		return "", nil
	}
	return base64.StdEncoding.EncodeToString(blob), nil
}

// readKeychain shells out to `security find-generic-password -s
// "<service>" -w`. Stderr suppressed — `security` prints "could
// not be found in keychain" when the entry is missing, which is
// a normal "fall through to file" case, not a real error.
//
// Returns (blob, nil) on found, (nil, nil) on not-found, and a
// real error only when `security` itself fails to run.
func readKeychain() ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", MacOSKeychainService, "-w")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// 44 = item not found; treat as empty-result.
			return nil, nil
		}
		return nil, fmt.Errorf("security: %w", err)
	}
	return []byte(strings.TrimSpace(string(out))), nil
}

// readFile reads ~/.claude/.credentials.json verbatim. Returns
// (nil, nil) when the file doesn't exist — that's a normal
// "operator doesn't have creds on this host" case.
func readFile() ([]byte, error) {
	path := CredentialsFilePath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return []byte(strings.TrimSpace(string(b))), nil
}
