package main

// `social-notifier mcp` — same shape as social-agent / social-ledger
// mcp: stdio by default, --http :PORT for Streamable HTTP.
//
// Bare port (5570) and host-less colon-port (:5570) bind on all
// interfaces — the form to use when a containerised inner claude
// on host.docker.internal needs to reach you. Pass an explicit
// host (127.0.0.1:5570) to lock to loopback. MCP_AUTH_TOKEN env
// adds bearer auth on the HTTP path.

import (
	"flag"
	"fmt"
	"os"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	notifiermcp "github.com/jedi4ever/social-skills/internal/notifier/mcp"
	"github.com/jedi4ever/social-skills/internal/util/mcphttp"
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	httpAddr := fs.String("http", "", "if set, serve over Streamable HTTP on this bind addr instead of stdio. Bare port (5570) and host-less colon-port (:5570) both bind on all interfaces. MCP_AUTH_TOKEN env adds bearer auth.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	addr := strings.TrimSpace(*httpAddr)
	httpMode := addr != ""
	if httpMode && !strings.Contains(addr, ":") {
		addr = ":" + addr
	}

	srv := notifiermcp.NewServer(notifiermcp.Config{Version: Version})

	if httpMode {
		token := strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN"))
		if token == "" {
			fmt.Fprintf(os.Stderr,
				"social-notifier mcp: WARNING — no MCP_AUTH_TOKEN set, every request accepted unauthenticated.\n"+
					"  Loopback-only addrs are fine; set MCP_AUTH_TOKEN before exposing on a routable interface.\n")
		} else {
			fmt.Fprintf(os.Stderr, "social-notifier mcp: bearer-token auth enabled (MCP_AUTH_TOKEN env)\n")
		}
		fmt.Fprintf(os.Stderr, "social-notifier mcp: listening on %s (Streamable HTTP)\n", addr)
		return mcphttp.Serve(addr, srv, mcphttp.Options{
			Service: "social-notifier-mcp",
			Version: Version,
			Token:   token,
		})
	}

	if err := mcpserver.ServeStdio(srv); err != nil {
		return fmt.Errorf("mcp stdio: %w", err)
	}
	return nil
}
