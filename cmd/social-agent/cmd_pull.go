package main

// Operator-facing file pull from a session's /artifacts (claude's
// outbox). /workspace is a separate concept — the optionally-
// bind-mounted cwd where claude works; only /artifacts comes back
// over the wire.
//
//   social-agent pull <id>                    pull whole tree to ./
//   social-agent pull <id> --to DIR           ... to DIR
//   social-agent pull <id> <path>             pull one file → ./<basename>
//   social-agent pull <id> <path> --to FILE   ... to FILE
//
//   social-agent rm-file <id> <path>          remove one file from container
//
// Always uses HTTP. Local docker exercises the same code path
// daytona will use, so HTTP-layer regressions show up at dev time.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jedi4ever/social-skills/internal/agent"
	"github.com/jedi4ever/social-skills/internal/agent/artifacts"
)

func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	provider := fs.String("provider", "docker", "substrate")
	to := fs.String("to", "", "destination dir (whole tree) or file (single path); default: ./")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("pull: <session-id> required")
	}
	id := fs.Arg(0)
	pathArg := ""
	if fs.NArg() > 1 {
		pathArg = fs.Arg(1)
	}

	prov, err := buildProvider(*provider)
	if err != nil {
		return err
	}
	ctx, cancel := signalCtx()
	defer cancel()

	url, err := artifactsURLFor(ctx, prov, id)
	if err != nil {
		return err
	}
	c := &artifacts.Client{BaseURL: url}

	if pathArg != "" {
		// Single-file pull. Default destination is the basename
		// in the cwd; --to can override to a specific path.
		dst := *to
		if dst == "" {
			dst = filepath.Base(pathArg)
		}
		if err := c.GetTo(ctx, pathArg, dst); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "pulled %s → %s\n", pathArg, dst)
		return nil
	}

	// Whole-tree pull.
	dest := *to
	if dest == "" {
		dest = "."
	}
	count, bytes, err := c.PullAll(ctx, dest)
	if err != nil {
		return err
	}
	if count == 0 {
		fmt.Fprintln(os.Stderr, "(artifacts empty)")
		return nil
	}
	fmt.Fprintf(os.Stderr, "pulled %d files (%s) → %s\n", count, humanBytes(bytes), dest)
	return nil
}

func cmdRmFile(args []string) error {
	fs := flag.NewFlagSet("rm-file", flag.ContinueOnError)
	provider := fs.String("provider", "docker", "substrate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("rm-file: <session-id> <path> required")
	}
	id := fs.Arg(0)
	path := fs.Arg(1)
	prov, err := buildProvider(*provider)
	if err != nil {
		return err
	}
	ctx, cancel := signalCtx()
	defer cancel()
	url, err := artifactsURLFor(ctx, prov, id)
	if err != nil {
		return err
	}
	c := &artifacts.Client{BaseURL: url}
	return c.Delete(ctx, path)
}

// artifactsURLFor finds the ArtifactsURL of a session by looking
// it up in the provider's List output. Cleaner than asking the
// provider for "give me this one session" (which would add a
// Get method); List is the existing surface.
func artifactsURLFor(ctx context.Context, prov agent.Provider, id string) (string, error) {
	sessions, err := prov.List(ctx)
	if err != nil {
		return "", err
	}
	// Allow short-form ids (first 12 chars) — same prefix-match
	// docker uses. If multiple sessions match, error out so the
	// operator picks.
	matches := matchSessions(sessions, id)
	if len(matches) == 0 {
		return "", fmt.Errorf("no session matches %q (see `social-agent ls`)", id)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple sessions match %q — use the full id", id)
	}
	if matches[0].ArtifactsURL == "" {
		return "", fmt.Errorf("session %s has no ArtifactsURL — provider didn't publish port 5563", short(matches[0].ID))
	}
	return matches[0].ArtifactsURL, nil
}

// matchSessions does prefix-match on container IDs in either
// direction — operators may have copied either the short form
// (12-char from `ls`) or the full form (64-char from `up`); both
// should resolve. We require at least 8 chars on either side to
// avoid false positives at very short prefixes.
func matchSessions(sessions []agent.Session, idOrPrefix string) []agent.Session {
	var out []agent.Session
	for _, s := range sessions {
		if s.ID == idOrPrefix || hasPrefix(s.ID, idOrPrefix) || hasPrefix(idOrPrefix, s.ID) {
			out = append(out, s)
		}
	}
	return out
}

func hasPrefix(s, p string) bool {
	if len(p) < 8 || len(s) < len(p) {
		return false
	}
	return s[:len(p)] == p
}

// humanBytes formats a byte count for the "pulled N files (X)"
// log line. Coarse — KB/MB/GB only — to keep the line short.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/(1024*1024*1024))
	}
}
