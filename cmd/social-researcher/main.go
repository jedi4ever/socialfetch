// social-researcher — one-line shell into a containerized research
// environment.
//
// Spawns a docker container backed by the social-skills-agent image
// (which already bundles social-fetch / social-ledger / social-browser /
// social-agent at /usr/local/bin) with the operator's cwd bind-mounted
// at /workspace and the operator's API-key envs auto-passed through.
//
// Two modes:
//
//	social-researcher run            → drops into /bin/bash inside the
//	                                   container. Manual workflow:
//	                                   `social-fetch fetch <url>`, edit
//	                                   files, run anything in-container.
//
//	social-researcher run --claude   → starts `claude` (the Claude Code
//	                                   TUI) with --mcp-config pre-wired
//	                                   to social-ledger mcp + social-agent
//	                                   mcp. Operator gets the chat UI with
//	                                   both tool surfaces ready, including
//	                                   recursive social_agent_run.
//
// `--claude` mode mounts `/var/run/docker.sock` so the inner social-agent
// MCP can spawn nested containers — pass `--no-docker-sock` to opt out
// (at the cost of disabling agent fan-out). Yolo mode
// (`--dangerously-skip-permissions`) is on by default in claude mode
// because the operator is driving directly.
//
// Mirrors the social-fetch / social-ledger / social-browser / social-agent
// CLI shape: subcommand dispatch, version constant in lockstep.
package main

import (
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Version is held in lockstep with the rest of the binaries +
// the claude-desktop / claude-code / marketplace manifests.
// See CLAUDE.md "Versioning".
const Version = "0.22.0"

func main() {
	dotenv.LoadAuto()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "social-researcher:", err)
		os.Exit(1)
	}
}

// run dispatches the top-level subcommand. Mirrors the small,
// switch-on-args[0] shape the other social-* binaries use.
func run(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "version", "--version", "-v":
		fmt.Println("social-researcher", Version)
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
	fmt.Fprintf(w, `social-researcher %s — interactive research shell in a container

USAGE
  social-researcher run [flags]
  social-researcher version
  social-researcher help

DESCRIPTION
  Spawns a docker container backed by social-skills-agent:%s with the
  operator's cwd bind-mounted at /workspace. All four social-* binaries
  are on PATH inside the container.

  Default: drops into /bin/bash. Pass --claude to start the Claude Code
  TUI instead, with social-ledger and social-agent MCP servers wired in.

FLAGS (run)
  --workdir DIR        host path bind-mounted at /workspace (default: cwd)
  --image TAG          docker image to run (default: social-skills-agent:%s)
  --claude             start `+"`claude`"+` TUI with --mcp-config registering
                       social-ledger + social-agent MCP servers (instead of
                       /bin/bash). Implies --dangerously-skip-permissions.
  --no-docker-sock     don't bind-mount /var/run/docker.sock into the
                       container. Only meaningful with --claude — the
                       socket mount is what makes mcp__agent__social_agent_run
                       able to spawn nested containers. Default: mount it.
  --env KEY=VAL        add an env var. Repeatable. Merged on top of the
                       PassthroughKeys auto-forwarded from your host env.
  --name NAME          explicit container name (default: auto-generated).

ENV PASSTHROUGH
  ANTHROPIC_API_KEY, OPENAI_API_KEY, PERPLEXITY_API_KEY, …, plus the
  social-* knobs (SOCIAL_LEDGER_DAEMON_URL, SOCIAL_FETCH_HEADLESS_DAEMON_URL,
  …) — full list in internal/agent/envpass.go's PassthroughKeys.

  Loopback URLs (127.0.0.1, localhost, 0.0.0.0) are auto-rewritten to
  host.docker.internal so the container reaches host services.

SECURITY
  --claude mode mounts /var/run/docker.sock by default. The inner agent
  thus has root-equivalent access to your host docker daemon. Pass
  --no-docker-sock for stricter sessions (at the cost of disabling
  recursive agent spawning).

EXAMPLES
  # Plain bash shell, repo at /workspace
  cd ~/dev/myproject && social-researcher run

  # Claude TUI with ledger + agent tools available
  social-researcher run --claude

  # Restricted: claude with ledger only, no agent fan-out
  social-researcher run --claude --no-docker-sock
`, Version, Version, Version)
}
