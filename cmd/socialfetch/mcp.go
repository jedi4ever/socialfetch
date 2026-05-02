// MCP subcommand: runs an MCP server over stdio so socialfetch can be
// installed as a Claude Desktop Extension (.mcpb) and driven via
// JSON-RPC instead of by shell-out.
//
// All tool handlers live in internal/mcp; this file is just the entry
// point that builds the registries and hands them to the server.
package main

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/mcp"
	"github.com/patrickdebois/social-skills/internal/platforms/linkedin"
	"github.com/patrickdebois/social-skills/internal/platforms/twitter"
)

func runMCP(args []string) error {
	for _, a := range args {
		switch a {
		case "-h", "--help":
			printMCPHelp()
			return nil
		default:
			return fmt.Errorf("mcp: unknown argument %q", a)
		}
	}

	fetchers, searchers := buildRegistries()
	askers := buildAskers()
	timelines := core.NewTimelineRegistry(
		twitter.NewXProvider(twitter.NewSearchProvider()),
		linkedin.NewLinkedInProvider(),
	)

	srv := mcp.NewServer(mcp.Config{
		Fetchers:           fetchers,
		Searchers:          searchers,
		Askers:             askers,
		Timelines:          timelines,
		DefaultAskChain:    defaultAskChain,
		DefaultSearchChain: defaultSearchChain,
		Version:            Version,
		BridgePort:         bridge.DefaultPort,
	})

	// ServeStdio reads JSON-RPC from os.Stdin and writes it to
	// os.Stdout. Anything we log on stdout corrupts the protocol —
	// the audit logger always writes to a file or stderr, so it's safe.
	return server.ServeStdio(srv)
}

func printMCPHelp() {
	fmt.Fprint(os.Stdout, `socialfetch mcp — run an MCP server on stdio

Usage:
  socialfetch mcp

When socialfetch is installed as a Claude Desktop Extension (.mcpb),
this is the entry point Claude Desktop launches. The server exposes
the existing fetch / search / ask / timeline / list_providers /
bridge_status capabilities as MCP tools.

For interactive use from a shell, prefer the regular subcommands
(socialfetch fetch, search, ask, timeline). The mcp subcommand
expects JSON-RPC framing on stdin/stdout — typing into it directly
will not work.

Configure API keys via environment variables (the same ones the
other subcommands read). Inside an installed .mcpb, Claude Desktop
populates them from the install dialog's user_config prompts.
`)
}
