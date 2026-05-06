package main

// `social-agent mcp` — run social-agent as an MCP server over
// stdio. Lets Claude Desktop, claude.ai (Custom Connectors), and
// Claude Code load social-agent as a tool, exposing run / up /
// exec / ls / down / pull / rm-file / harness-list.
//
// stdio-only in v1; HTTP / ngrok mirror is a follow-up if we
// need it. Same shape `social-fetch mcp` runs in by default.

import (
	"flag"
	"fmt"

	mcpserver "github.com/mark3labs/mcp-go/server"

	agentmcp "github.com/jedi4ever/social-skills/internal/agent/mcp"
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	image := fs.String("image", "", "default docker image:tag for sessions (default: social-skills-agent:<Version>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := agentmcp.Config{
		Version:      Version,
		DefaultImage: *image,
	}
	srv := agentmcp.NewServer(cfg)
	// ServeStdio reads JSON-RPC from stdin, writes to stdout.
	// Anything we log to stdout corrupts the protocol — handlers
	// log to stderr only.
	if err := mcpserver.ServeStdio(srv); err != nil {
		return fmt.Errorf("mcp stdio: %w", err)
	}
	return nil
}
