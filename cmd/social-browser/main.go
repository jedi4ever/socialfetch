// social-browser — pluggable browser-pool daemon that fronts a
// fleet of remote chromedp endpoints behind one local URL.
//
// Two top-level concerns, two top-level subcommand groups:
//
//	social-browser daemon ...     start / stop / status the local
//	                              daemon (default port :5560)
//
//	social-browser provider ...   manage backends per substrate.
//	                              `provider daytona up -n 3` etc.
//
// Clients (social-fetch, MCP server, anything that speaks the
// chromedp daemon protocol) point at the daemon's URL and never
// see provider-specific concerns like Daytona preview tokens or
// snapshot pushes — those stay inside the provider implementation.
//
// Replaces the earlier social-daytona binary, which has been
// removed in favour of this provider-pluggable layout.
package main

import (
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Version is held in lockstep with social-fetch / social-ledger
// and the claude-desktop / claude-code / marketplace manifests.
// See CLAUDE.md "Versioning".
const Version = "0.25.4"

func main() {
	dotenv.LoadAuto()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "social-browser:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "daemon":
		return cmdDaemon(args[1:])
	case "provider":
		return cmdProvider(args[1:])
	case "version", "--version", "-v":
		fmt.Println("social-browser", Version)
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
	fmt.Fprintf(w, `social-browser %s — pluggable browser-pool daemon

USAGE
  social-browser daemon <verb>      manage the local round-robin daemon
  social-browser provider <name>    manage backends for a substrate

DAEMON
  daemon start [--bind ADDR] --provider {daytona|local} [...]
                                          spawn the daemon (default :5560)
    daytona-only:  --pool N --id ID --verbose
    local-only:    --pool-size N --recycle-after N
  daemon stop                              stop a running daemon
  daemon status                            print fleet snapshot
  daemon run                               foreground (used by start)

PROVIDERS
  provider daytona up -n N        spawn N Daytona sandboxes
  provider daytona ls             list our sandboxes
  provider daytona down [<id>...] tear down by id, or all of ours
  provider daytona env [<id>]     print env exports for one sandbox
                                  (legacy: skip the daemon, point
                                  social-fetch directly at the URL)

ENVIRONMENT
  DAYTONA_API_KEY    bearer token (required for daytona provider)
  DAYTONA_ORG_ID     active organisation id
  DAYTONA_API_URL    API base URL (default: https://app.daytona.io/api)

  Auto-loaded from project-local .env if present.

EXAMPLES
  # Spin up 3 sandboxes and front them behind a local daemon
  social-browser provider daytona up -n 3
  social-browser daemon start

  # Point social-fetch at the daemon — no token needed
  export SOCIAL_FETCH_HEADLESS_DAEMON_URL=http://127.0.0.1:5560
  social-fetch fetch https://example.com   # round-robins across the fleet

  # Tear down
  social-browser daemon stop
  social-browser provider daytona down
`, Version)
}
