// social-daytona — orchestrate a fleet of social-skills sandboxes
// on Daytona.
//
// Wraps the Daytona REST API (direct HTTP — no Go SDK exists today)
// to build / push the social-skills container image as a Daytona
// snapshot and spin up N sandboxes from it. Each sandbox carries a
// per-instance tunnel URL (signed preview URL) so a remote MCP
// client (claude.ai, ChatGPT, Claude Desktop's remote-MCP support)
// can hit `https://<sig>-5558.proxy.daytona.work/mcp` directly.
//
// Auth: reads `DAYTONA_API_KEY` + `DAYTONA_ORG_ID` from process
// env (typically project-local `.env`). `DAYTONA_API_URL`
// overrides the default `https://app.daytona.io/api` for
// self-hosted Daytona installs.
//
// Why a separate binary instead of a `social-fetch daytona`
// subcommand: the operational concerns are different (managing
// remote infra vs. fetching content) and we want operators on
// machines without `social-fetch` installed to still be able to
// manage Daytona sandboxes. Same versioning lockstep though, so
// build/push/up/ls all advertise the same release identity.
package main

import (
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/util/dotenv"
)

// Version is held in lockstep with social-fetch / social-ledger.
// Bump together — see cmd/social-fetch/main.go for the canonical
// versioning rule + the make-check rule that enforces all five
// version fields agree before a commit lands.
const Version = "0.13.14"

func main() {
	// Pull DAYTONA_API_KEY / ORG_ID / API_URL out of any .env in
	// the cwd or the repo root before any subcommand runs. Same
	// shared resolver social-fetch / social-ledger use, so a
	// single .env serves all three binaries — operators don't
	// have to remember which tool needs `set -a; source .env`.
	dotenv.LoadAuto()

	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "social-daytona:", err)
		os.Exit(1)
	}
}

// run dispatches the top-level subcommand. Kept small on purpose
// — each subcommand owns its own flag parsing in cmd_<verb>.go.
func run(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "build":
		return cmdBuild(args[1:])
	case "push":
		return cmdPush(args[1:])
	case "up":
		return cmdUp(args[1:])
	case "ls", "list":
		return cmdLs(args[1:])
	case "down", "stop":
		return cmdDown(args[1:])
	case "logs":
		return cmdLogs(args[1:])
	case "version", "--version", "-v":
		fmt.Println("social-daytona", Version)
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
	fmt.Fprintf(w, `social-daytona %s — orchestrate social-skills sandboxes on Daytona

USAGE
  social-daytona <command> [flags] [args]

COMMANDS
  build              build the social-skills container image locally
                     (delegates to `+"`"+`make docker-build`+"`"+`)
  push               push the local image to Daytona as a snapshot
                     (delegates to `+"`"+`daytona snapshot push`+"`"+`)
  up -n N            create N sandboxes from the snapshot, print
                     per-instance tunnel URLs
  ls                 list our sandboxes (filtered by social-daytona label)
  down [<id>...]     delete sandboxes — by id, or all of ours when no
                     id is given
  logs <id>          tail combined daemon logs from one sandbox

  version            print version
  help               this message

ENVIRONMENT
  DAYTONA_API_KEY    bearer token (required)
  DAYTONA_ORG_ID     active organisation id (required for create / list)
  DAYTONA_API_URL    API base URL (default: https://app.daytona.io/api)

  Read from process env; project-local .env files are honoured if
  the operator sources them (`+"`"+`set -a; source .env; set +a`+"`"+`).

EXAMPLES
  social-daytona build && social-daytona push
  social-daytona up -n 3
  social-daytona ls
  social-daytona down                 # delete every sandbox we created
  social-daytona logs sb-12abf...
`, Version)
}
