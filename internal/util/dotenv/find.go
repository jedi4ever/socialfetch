package dotenv

import (
	"os"
	"path/filepath"
)

// Find walks parent directories from `start`, returning the absolute
// path of the first `.env` file it finds. Stops at `$HOME`,
// filesystem root, or after 4 levels — whichever comes first.
//
// The home-directory boundary is intentional: `~/.env` is too easy
// to land on by accident, and a stranger's global secrets file is
// the worst possible thing to load. Better to return "" and let
// the caller try a different starting point.
//
// Returns "" if nothing matches inside the search budget.
func Find(start string) string {
	const maxDepth = 4
	home, _ := os.UserHomeDir()
	dir := start
	for i := 0; i <= maxDepth; i++ {
		candidate := filepath.Join(dir, ".env")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		if home != "" && dir == home {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// LoadAuto runs the canonical socialfetch `.env` discovery: walks up
// from the current working directory and from the binary's location,
// loading the first `.env` found at each starting point. Existing
// shell env vars are never overridden (per Load semantics).
//
// This is what the `socialfetch` CLI calls at startup AND what every
// live_test.go in the repo uses, so the discovery rules stay
// consistent: a developer running `go test -tags=live ./...` from any
// subdir resolves credentials the same way the installed binary does.
//
// Returns the list of paths it actually loaded — handy for `--log`
// debug, but also fine to ignore.
func LoadAuto() []string {
	seen := make(map[string]bool)
	var loaded []string
	tryPath := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		if err := Load(p); err == nil {
			loaded = append(loaded, p)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		tryPath(Find(cwd))
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		tryPath(Find(filepath.Dir(exe)))
	}
	return loaded
}
