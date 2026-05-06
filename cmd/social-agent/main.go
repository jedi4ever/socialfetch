// social-agent — sandboxed coding-agent runner.
//
// Spins up Claude Code (default harness; codex / gemini etc. plug
// in later) inside a docker container, runs prompts, manages
// session lifecycles. Bundles the social-skills binaries
// (social-fetch / social-ledger / social-browser) inside the
// container so the agent can shell out to them — typically
// pointed at the operator's social-browser daemon for the
// chromedp pool.
//
// Two top-level concerns, two ways to invoke:
//
//	social-agent {run|up|exec|down|ls}      shortcut form
//	                                         (defaults to
//	                                         --provider docker)
//
//	social-agent provider docker <verb>     explicit form (lets
//	                                         future providers like
//	                                         daytona slot in
//	                                         without breaking the
//	                                         short surface)
//
//	social-agent harness {list}             which agent CLIs are
//	                                         built in
//
// The shortcuts share dispatch with the provider namespace —
// `social-agent run "..."` ≈ `social-agent provider docker run "..."`.
//
// Versioning is locked to social-fetch / social-ledger /
// social-browser (see CLAUDE.md "Versioning"). Bumping one bumps
// all four binaries plus the three manifests.
package main

import (
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Version is held in lockstep with the rest of the binaries +
// the claude-desktop / claude-code / marketplace manifests. See
// CLAUDE.md "Versioning".
const Version = "0.16.1"

func main() {
	dotenv.LoadAuto()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "social-agent:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "up", "start":
		return cmdUp(args[1:])
	case "down", "stop":
		return cmdDown(args[1:])
	case "exec":
		return cmdExec(args[1:])
	case "ls", "list":
		return cmdLs(args[1:])
	case "pull":
		return cmdPull(args[1:])
	case "rm-file":
		return cmdRmFile(args[1:])
	case "daemon":
		return cmdDaemon(args[1:])
	case "provider":
		return cmdProvider(args[1:])
	case "harness":
		return cmdHarness(args[1:])
	case "artifacts":
		return cmdArtifacts(args[1:])
	case "version", "--version", "-v":
		fmt.Println("social-agent", Version)
		return nil
	case "help", "-h", "--help":
		printHelp(os.Stdout)
		return nil
	default:
		printHelp(os.Stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printHelp(w *os.File) {
	fmt.Fprintf(w, `social-agent %s — sandboxed claude-code sessions

USAGE
  social-agent run "<prompt>" [--output DIR]
                                         one-shot: up + run prompt + (if
                                         --output) pull /artifacts + down
  social-agent up [--workdir DIR]        create a persistent session
  social-agent exec <id> [-- cmd...]     enter a session (PTY shell or run cmd)
  social-agent down [<id>...]            tear down sessions (empty = all of ours)
  social-agent ls                        list our sessions
  social-agent pull <id> [<path>] [--to PATH]
                                         pull /artifacts (or one file) to host
  social-agent rm-file <id> <path>       remove one file from /artifacts

  social-agent daemon {start|stop|status|run}
                                         long-running daemon — same shape
                                         as social-browser daemon (HTTP API
                                         on :5562 by default)
  social-agent provider docker {build|up|ls|exec|down|run}
                                         explicit form; future providers
                                         (daytona) plug in here

  social-agent harness list              available coding-agent CLIs
                                         (today: claude-code)

ENVIRONMENT
  ANTHROPIC_API_KEY                       claude-code auth (env passthrough)
  CLAUDE_OAUTH_CREDENTIALS                base64 OAuth blob (alt to API key)
  SOCIAL_FETCH_HEADLESS_DAEMON_URL        passed into the container so the
                                          inner social-fetch routes through
                                          the operator's chromedp pool
  SOCIAL_LEDGER_DAEMON_URL                same, for the ledger daemon

  Auto-loaded from project-local .env if present.

EXAMPLES
  # Build the agent image (host's native arch)
  social-agent provider docker build

  # One-shot prompt against the current dir
  ANTHROPIC_API_KEY=... social-agent run --workdir . "summarise README.md"

  # Persistent session for a multi-step task
  social-agent up --workdir .            # → prints session id
  social-agent exec <id>                 # → drops into claude PTY
  social-agent down <id>                 # → cleans up

  # Daemon mode (mirror of social-browser daemon)
  social-agent daemon start
  curl -s http://127.0.0.1:5562/status

`, Version)
}
