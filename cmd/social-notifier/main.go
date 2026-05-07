// social-notifier — sends short notifications out of a research
// session via pluggable providers (Slack today; Discord / email /
// webhook / PagerDuty are future provider files).
//
// Same shape as the rest of the social-* binaries: subcommand
// dispatch, version constant in lockstep, dotenv auto-load, MCP
// surface available via `social-notifier mcp` (stdio or --http).
//
// Aimed at status updates from a long-running agent run — "report
// is ready", "job N/M done", "ran into an auth issue" — with the
// inner claude inside a social-researcher / social-agent container
// shelling out via Bash today and via MCP (`social_notifier_post`)
// once it's in the inner --mcp-config.
//
// Versioning is locked to social-fetch / social-ledger /
// social-browser / social-agent / social-researcher (see CLAUDE.md
// "Versioning"). Bumping one bumps all six binaries plus the
// three manifests.
package main

import (
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/util/dotenv"

	// Side-effect import: registers the slack provider in
	// internal/notifier's registry.
	_ "github.com/jedi4ever/social-skills/internal/notifier/slack"
)

// Version is held in lockstep with the rest of the binaries +
// the claude-desktop / claude-code / marketplace manifests.
// See CLAUDE.md "Versioning".
const Version = "0.27.1"

func main() {
	dotenv.LoadAuto()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "social-notifier:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "post":
		return cmdPost(args[1:])
	case "providers":
		return cmdProviders(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "version", "--version", "-v":
		fmt.Println("social-notifier", Version)
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
	fmt.Fprintf(w, `social-notifier %s — pluggable notification CLI

USAGE
  social-notifier post --provider <name> [--channel <id>] "<message>"
  social-notifier post --provider <name> [--channel <id>] --json '<blocks>'
  social-notifier providers list
  social-notifier mcp [--http :PORT]
  social-notifier version
  social-notifier help

SUBCOMMANDS
  post        send a single message via the named provider
  providers   list available providers
  mcp         expose post as an MCP tool (stdio default; --http
              :PORT serves Streamable HTTP, same shape as
              social-agent / social-ledger mcp).

PROVIDERS
  slack       chat.postMessage with a Bot Token. Reads
              SLACK_BOT_TOKEN (required) and SLACK_DEFAULT_CHANNEL
              (optional default for --channel).

ENV PASSTHROUGH
  SLACK_BOT_TOKEN          xoxb-… bot token from Slack app config
  SLACK_DEFAULT_CHANNEL    channel id or name; --channel overrides

  Auto-loaded from project-local .env via dotenv.LoadAuto().

EXAMPLES
  social-notifier post --channel "#research" "report ready: /artifacts/foo.md"
  social-notifier post --channel C0123 --json '{"blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*done*"}}]}'
  social-notifier mcp --http :5570
`, Version)
}
