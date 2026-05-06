package main

// `social-agent artifacts serve` — runs the in-container HTTP
// server that exposes /artifacts (claude's outbox) to the
// operator. Invoked by docker-agent-entrypoint.sh at container
// boot, never directly by the operator.
//
// Top-level operator-facing pull / rm-file verbs live in
// cmd_pull.go (so they can stay flat: `social-agent pull <id>`,
// `social-agent rm-file <id> <path>`). Keeping `artifacts` as
// the namespace for the in-container `serve` mode keeps the
// operator-facing CLI surface uncluttered.

import (
	"flag"
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/agent/artifacts"
)

func cmdArtifacts(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("artifacts: <verb> required (try: serve)")
	}
	switch args[0] {
	case "serve":
		return runArtifactsServe(args[1:])
	default:
		return fmt.Errorf("artifacts: unknown verb %q (try: serve)", args[0])
	}
}

// runArtifactsServe binds the artifacts HTTP server. Logs each
// request line to stderr — entrypoint redirects that to
// /tmp/artifacts-server.log so a failed pull is debuggable
// without a separate /logs surface.
func runArtifactsServe(args []string) error {
	fs := flag.NewFlagSet("artifacts serve", flag.ContinueOnError)
	root := fs.String("root", "/artifacts", "directory to serve")
	bind := fs.String("bind", "0.0.0.0:5563", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s := &artifacts.Server{
		Root: *root,
		Logf: func(format string, a ...any) { fmt.Fprintf(os.Stderr, "artifacts-server: "+format+"\n", a...) },
	}
	return s.Run(*bind)
}
