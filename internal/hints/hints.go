// Package hints aggregates per-platform "quirks & gotchas" markdown
// (rate limits, auth shapes, time-window caps, anything non-obvious
// from the SKILL.md or the bare auth-hint string in `list`). Each
// platform package owns its own hints.md, embedded as a string via
// //go:embed; this package is the central registry that makes them
// queryable from `social-fetch hints [name]` (CLI) and the
// social_fetch_hints tool (MCP).
//
// Convention: any platform with non-obvious behaviour ought to have
// a hints.md. To add one:
//
//  1. Create internal/platforms/<name>/hints.md
//  2. Create internal/platforms/<name>/hints.go that embeds it as
//     `var Hints string`
//  3. Register it in the map below
//
// Adding a new platform with no hints is fine — the registry only
// includes what's been written.
package hints

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jedi4ever/social-skills/internal/platforms/linkedin"
	"github.com/jedi4ever/social-skills/internal/platforms/medium"
	"github.com/jedi4ever/social-skills/internal/platforms/reddit"
	"github.com/jedi4ever/social-skills/internal/platforms/substack"
	"github.com/jedi4ever/social-skills/internal/platforms/twitter"
)

// platformHints maps a platform name (matching the registry's
// canonical name, plus common aliases) to its hints markdown. Alias
// entries point at the same string so `hints x` and `hints twitter`
// both resolve — mirrors the search/fetch alias behaviour.
var platformHints = map[string]string{
	"x":        twitter.Hints,
	"twitter":  twitter.Hints,
	"linkedin": linkedin.Hints,
	"medium":   medium.Hints,
	"substack": substack.Hints,
	"reddit":   reddit.Hints,
}

// Get returns the hints markdown for a platform. ok=false when no
// hints are registered for that name; caller decides whether to
// treat that as an error or fall through to a "no hints written
// yet" message.
func Get(name string) (md string, ok bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	md, ok = platformHints[name]
	if ok && strings.TrimSpace(md) == "" {
		return "", false
	}
	return md, ok
}

// Catalog returns canonical platform names (alphabetically) that
// have hints registered. Aliases are deduped — when two keys map to
// the same hints string, the shortest name wins ("x" over "twitter")
// since that's the canonical fetcher name in this codebase.
func Catalog() []string {
	canonicalFor := map[string]string{}
	for name, hints := range platformHints {
		if hints == "" {
			continue
		}
		key := hints
		if cur, ok := canonicalFor[key]; !ok || len(name) < len(cur) {
			canonicalFor[key] = name
		}
	}
	out := make([]string, 0, len(canonicalFor))
	for _, name := range canonicalFor {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// MustGet returns the hints markdown or a constructed error
// describing what's available — handy for the CLI's "no hints for
// X" branch. Intentionally returns the error fully formatted so
// callers don't need to import this package's catalog separately.
func MustGet(name string) (string, error) {
	md, ok := Get(name)
	if !ok {
		return "", fmt.Errorf("no hints for %q (try one of: %s)",
			name, strings.Join(Catalog(), ", "))
	}
	return md, nil
}

// All concatenates every registered platform's hints into a single
// markdown document, with a separator between each platform so the
// reader can scan top-down. Used by `social-fetch hints` (no
// argument) and the MCP tool's empty-platform branch — the agent
// gets the full reference in one tool call rather than a list of
// names + a follow-up call per platform.
func All() string {
	var b strings.Builder
	for i, name := range Catalog() {
		md, ok := Get(name)
		if !ok || strings.TrimSpace(md) == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString(md)
		// Markdown content rarely ends with a trailing newline; add
		// one so the next platform's heading lands on its own line.
		if !strings.HasSuffix(md, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}
