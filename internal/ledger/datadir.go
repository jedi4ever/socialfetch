package ledger

// Shared data-dir resolution helpers. Mirrors the logic in
// cmd/social-ledger/main.go's dataDir() so callers (MCP screenshot
// tool, CLI screenshot writer, future companion-file producers)
// can land output in the same project directory the running
// ledger daemon serves files from.
//
// Why duplicate it here rather than import cmd/social-ledger:
// internal packages can't import cmd/. Keeping a small standalone
// resolver is cheaper than restructuring. Kept in sync with the
// CLI version manually — both must agree on the projects/<NAME>/
// subdir or the daemon and clients won't see the same files.

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectEnv selects which sub-bucket under <base>/projects/ this
// process operates in. Match what cmd/social-ledger/main.go uses.
const ProjectEnv = "SOCIAL_LEDGER_PROJECT"

// DefaultProject is the bucket name used when ProjectEnv is unset
// — matches cmd/social-ledger's DefaultProject.
const DefaultProject = "social_fetch"

// BaseDataDir is the unprojected base — typically
// $XDG_DATA_HOME/social-ledger or ~/.local/share/social-ledger.
// SOCIAL_LEDGER_DIR overrides everything when set.
func BaseDataDir() (string, error) {
	if d := strings.TrimSpace(os.Getenv(DirEnv)); d != "" {
		return d, nil
	}
	if d := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); d != "" {
		return filepath.Join(d, "social-ledger"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "social-ledger"), nil
}

// ProjectDataDir applies the projects/<NAME>/ subdir layered on
// BaseDataDir. The name comes from SOCIAL_LEDGER_PROJECT, or
// DefaultProject when unset. Sanitised against path-traversal
// the same way cmd/social-ledger does.
func ProjectDataDir() (string, error) {
	base, err := BaseDataDir()
	if err != nil {
		return "", err
	}
	proj := strings.TrimSpace(os.Getenv(ProjectEnv))
	if proj == "" {
		proj = DefaultProject
	}
	return filepath.Join(base, "projects", sanitizeProjectName(proj)), nil
}

// ScreenshotsDir returns the dir where the screenshot tools write
// PNG output. Always under the per-project dir so the running
// ledger daemon can serve them via GET /screenshots/<file>. Created
// on first call (idempotent MkdirAll).
//
// Returns an error only when neither $SOCIAL_LEDGER_DIR nor a
// derivable home dir is available. Most callers fall back to
// os.TempDir() in that case so screenshot capture still works in
// stripped-down environments.
func ScreenshotsDir() (string, error) {
	base, err := ProjectDataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// sanitizeProjectName accepts the project name as configured but
// enforces filesystem safety: alnum + dash + underscore only.
// Stripped chars become `-`, empty input becomes "default".
func sanitizeProjectName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "default"
	}
	return out
}
