// social-ui — small local web UI for parallel research sessions.
//
// Each browser tab maps to one social_agent session: the operator
// types a prompt, the UI POSTs it as a `social_agent_run`, polls
// `social_agent_run_status` until done, renders the events +
// response + artifacts. Multiple tabs run in parallel against the
// same host social-agent MCP.
//
// Loopback-only by default — single-operator local tool, no auth
// on its own routes (the operator owns the terminal that started
// it). Reads MCP_AUTH_TOKEN from env to authenticate against the
// host social-agent MCP, same way social-researcher does.
//
// Same lockstep versioning as the other social-* binaries; ships
// as a sibling under dist/.
package main

import (
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Version is held in lockstep with the rest of the binaries +
// the claude-desktop / claude-code / marketplace manifests.
// See CLAUDE.md "Versioning".
const Version = "0.27.1"

func main() {
	dotenv.LoadAuto()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "social-ui:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "version", "--version", "-v":
		fmt.Println("social-ui", Version)
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
	fmt.Fprintf(w, `social-ui %s — local web UI for parallel research sessions

USAGE
  social-ui serve [flags]
  social-ui version
  social-ui help

DESCRIPTION
  Starts an HTTP server on a loopback port. Open the URL in your
  browser; each tab is one social_agent session. Send a prompt,
  watch events stream in, see the final response + artifacts.

FLAGS (serve)
  --bind ADDR          loopback bind addr (default: 127.0.0.1:5571)
  --agent-mcp-url URL  the host social-agent MCP HTTP endpoint
                       (default chain: $SOCIAL_AGENT_MCP_URL,
                       then http://127.0.0.1:5562/mcp).

ENV
  MCP_AUTH_TOKEN       sent as Authorization: Bearer when calling
                       the host MCP. Required when the MCP has a
                       token configured.
  SOCIAL_AGENT_MCP_URL fallback for --agent-mcp-url.

  Auto-loaded from project-local .env via dotenv.LoadAuto().

EXAMPLES
  # Default: connects to host social-agent on 127.0.0.1:5562
  ./dist/social-ui serve

  # Explicit endpoint
  ./dist/social-ui serve --agent-mcp-url http://falcon-pdb-plus.taild6bbf3.ts.net:5562/mcp
`, Version)
}
