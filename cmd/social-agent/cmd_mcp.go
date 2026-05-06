package main

// `social-agent mcp` — run social-agent as an MCP server.
//
// Two transports:
//
//   stdio (default)   Claude Desktop / claude.ai / Claude Code spawn
//                     the binary as a child and speak JSON-RPC over the
//                     pipes — the shape .mcp.json registers.
//
//   --http :PORT      Streamable HTTP on the supplied bind addr (e.g.
//                     :5562 or 127.0.0.1:5562). Lets remote MCP
//                     clients call social_agent_run / etc. Most
//                     important consumer: an inner claude running
//                     inside a social-researcher container — it
//                     points at http://host.docker.internal:PORT/mcp
//                     and never needs the host docker socket
//                     bind-mounted.
//
// MCP_AUTH_TOKEN env gates /mcp on the HTTP path. Loopback-only
// listens can skip the token; expose to host.docker.internal from a
// containerised inner claude and a token is wise.

import (
	"flag"
	"fmt"
	"os"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	agentmcp "github.com/jedi4ever/social-skills/internal/agent/mcp"
	"github.com/jedi4ever/social-skills/internal/util/mcphttp"
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	image := fs.String("image", "", "default docker image:tag for sessions (default: social-skills-agent:<Version>)")
	httpAddr := fs.String("http", "", "if set, serve over Streamable HTTP on this bind addr (e.g. :5562 or 127.0.0.1:5562) instead of stdio. /mcp is the protocol endpoint; / and /health are unauthenticated status probes. MCP_AUTH_TOKEN env adds bearer auth.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := agentmcp.Config{
		Version:      Version,
		DefaultImage: *image,
	}
	srv := agentmcp.NewServer(cfg)

	if strings.TrimSpace(*httpAddr) != "" {
		token := strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN"))
		if token == "" {
			fmt.Fprintf(os.Stderr,
				"social-agent mcp: WARNING — no MCP_AUTH_TOKEN set, every request accepted unauthenticated.\n"+
					"  Loopback-only addrs are fine; set MCP_AUTH_TOKEN before exposing on a routable interface.\n")
		} else {
			fmt.Fprintf(os.Stderr, "social-agent mcp: bearer-token auth enabled (MCP_AUTH_TOKEN env)\n")
		}
		fmt.Fprintf(os.Stderr, "social-agent mcp: listening on %s (Streamable HTTP)\n", *httpAddr)
		return mcphttp.Serve(*httpAddr, srv, mcphttp.Options{
			Service: "social-agent-mcp",
			Version: Version,
			Token:   token,
		})
	}

	// ServeStdio reads JSON-RPC from stdin, writes to stdout.
	// Anything we log to stdout corrupts the protocol — handlers
	// log to stderr only.
	if err := mcpserver.ServeStdio(srv); err != nil {
		return fmt.Errorf("mcp stdio: %w", err)
	}
	return nil
}
