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
const Version = "0.23.1"

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
  Spawns a docker container backed by social-skills-researcher:latest
  (built by 'make researcher-build') with the operator's cwd bind-
  mounted at /workspace. All four social-* binaries are on PATH inside
  the container.

  Default: drops into /bin/bash. Pass --claude to start the Claude Code
  TUI instead, with social-ledger and social-agent MCP servers wired in.

FLAGS (run)
  --workdir DIR        host path bind-mounted at /workspace (default: cwd)
  --image TAG          docker image to run (default: social-skills-researcher:latest)
  --claude             start `+"`claude`"+` TUI with --mcp-config registering
                       social-ledger + social-agent MCP servers (instead of
                       /bin/bash). Implies --dangerously-skip-permissions.
  --agent-mcp-url URL  override the agent MCP HTTP endpoint (default:
                       http://host.docker.internal:5562/mcp). Falls back
                       to $SOCIAL_AGENT_MCP_URL when unset.
  --ledger-mcp-url URL override the ledger MCP HTTP endpoint (default:
                       http://host.docker.internal:5557/mcp). Falls back
                       to $SOCIAL_LEDGER_MCP_URL when unset.
  --stdio              force stdio MCP for both servers — inner claude
                       spawns /usr/local/bin/social-{agent,ledger} mcp
                       inside the container instead of dialing host HTTP.
                       Use when no host MCP servers are running.
  --no-docker-sock     don't bind-mount /var/run/docker.sock into the
                       container. Only relevant in --stdio mode (HTTP mode
                       doesn't need it — host social-agent already has
                       docker access).
  --env KEY=VAL        add an env var. Repeatable. Merged on top of the
                       PassthroughKeys auto-forwarded from your host env.
  --name NAME          explicit container name (default: auto-generated).

ENV PASSTHROUGH
  ANTHROPIC_API_KEY, OPENAI_API_KEY, PERPLEXITY_API_KEY, …, plus the
  social-* knobs (SOCIAL_LEDGER_DAEMON_URL, SOCIAL_FETCH_HEADLESS_DAEMON_URL,
  …) — full list in internal/agent/envpass.go's PassthroughKeys.

  Loopback URLs (127.0.0.1, localhost, 0.0.0.0) are auto-rewritten to
  host.docker.internal so the container reaches host services.

  SOCIAL_AGENT_MCP_URL / SOCIAL_LEDGER_MCP_URL — fallbacks for the
  --agent-mcp-url / --ledger-mcp-url flags. Set in .env once and
  every social-researcher run --claude picks them up.

  MCP_AUTH_TOKEN — sent as Authorization: Bearer in the inner
  claude's --mcp-config when an HTTP endpoint is registered. Same
  value the host-side social-agent mcp --http / social-ledger mcp
  --http validate against.

SECURITY
  Default --claude mode talks to host MCP servers over HTTP — no
  docker.sock mount needed (the host's social-agent already has docker
  access). When using --stdio, the inner claude spawns its own MCP
  binaries inside the container; for nested-agent spawning to work
  there, /var/run/docker.sock is bind-mounted by default. Pass
  --no-docker-sock for stricter --stdio sessions.

TAILSCALE (alternative to host.docker.internal)
  The researcher image ships the tailscale binaries. Useful when:

    - Running researcher on a remote host where host.docker.internal
      doesn't bridge to your laptop (Daytona, a different machine).
    - Reaching MCP servers (or anything else) on your tailnet by
      tailnet-DNS name instead of an IP that varies per setup.

  Setup (once): grab a pre-auth key from
    https://login.tailscale.com/admin/settings/keys
  Tick "Ephemeral" when you create it — researcher containers come
  and go, so each tailscale-up registers a fresh device; ephemeral
  keys + the --ephemeral flag tell Tailscale to auto-prune the node
  ~5 min after disconnect, so your tailnet doesn't fill with dead
  research-* entries. Reusable + tag it (e.g. tag:research) so the
  same key works across many runs.

  Add the key to your shell or .env as TS_AUTHKEY=tskey-auth-...;
  PassthroughKeys forwards it into the container automatically.

  Inside the container, bring tailscale up in userspace mode (no
  NET_ADMIN / /dev/net/tun needed). The default tailscaled socket
  path /var/run/tailscale/ doesn't exist + isn't writable for the
  non-root agent user — we point both processes at /tmp instead.

    tailscaled \
      --tun=userspace-networking \
      --socket=/tmp/tailscaled.sock \
      --state=/tmp/tailscaled.state &
    tailscale --socket=/tmp/tailscaled.sock up \
      --authkey="$TS_AUTHKEY" \
      --ephemeral \
      --hostname=research-$(hostname)
    tailscale --socket=/tmp/tailscaled.sock status

  Then reach a host on your tailnet by name:
    curl http://laptop.tailnet.ts.net:5562/mcp

  Or point the researcher's --agent-mcp-url / --ledger-mcp-url at the
  tailnet hostnames before exec'ing into the container so claude uses
  them directly.

  Notes: in userspace mode, outbound to tailnet hosts works
  transparently. For inbound (other tailnet hosts reaching the
  container), use `+"`tailscale serve`"+` (also userspace-mode-friendly).
  Auth keys are single-use unless you mark them reusable in the admin UI.

EXAMPLES
  # Plain bash shell, repo at /workspace
  cd ~/dev/myproject && social-researcher run

  # Claude TUI with ledger + agent tools, talking to host MCP HTTP
  # endpoints. Prereq: in another tab,
  #   social-agent mcp --http :5562
  #   social-ledger mcp --http :5557
  social-researcher run --claude

  # Same with bearer auth — set MCP_AUTH_TOKEN in your .env
  MCP_AUTH_TOKEN=secret social-researcher run --claude

  # Override the URLs explicitly (e.g. host MCPs on a different machine)
  social-researcher run --claude \
    --agent-mcp-url  https://research.example.com/agent/mcp \
    --ledger-mcp-url https://research.example.com/ledger/mcp

  # Self-contained: stdio MCP, binaries spawned inside the container.
  # Use when you don't have host HTTP servers running.
  social-researcher run --claude --stdio

  # stdio mode without docker.sock (no nested agent spawning)
  social-researcher run --claude --stdio --no-docker-sock
`, Version)
}
