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
	"net/http"
	"os"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	agentmcp "github.com/jedi4ever/social-skills/internal/agent/mcp"
	"github.com/jedi4ever/social-skills/internal/util/mcphttp"
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	image := fs.String("image", "", "default docker image:tag for sessions (default: social-skills-agent:<Version>)")
	httpAddr := fs.String("http", "", "if set, serve over Streamable HTTP on this bind addr instead of stdio. Bare port (5562) and host-less colon-port (:5562) both bind on all interfaces — the form to use when a containerised inner claude on host.docker.internal needs to reach you. Pass an explicit host (127.0.0.1:5562) to lock to loopback. MCP_AUTH_TOKEN env adds bearer auth.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	addr := strings.TrimSpace(*httpAddr)
	httpMode := addr != ""
	// Normalize a bare port (e.g. "5562") into ":5562" so it binds
	// on all interfaces. Without this, Go's net.Listen treats
	// "5562" as a hostname-only with no port and errors. We could
	// reject the input, but the bare-port form is what every
	// "default port" docs example tends to suggest, so accept it.
	if httpMode && !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	// In HTTP mode, derive an HMAC signing key from MCP_AUTH_TOKEN
	// for self-authorising artifact URLs. Same token = same key,
	// so signed URLs survive a daemon restart. No token = no
	// signing → URLs only auth'd by the bearer header (operator
	// curl flow).
	token := ""
	var signKey []byte
	if httpMode {
		token = strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN"))
		signKey = agentmcp.DeriveArtifactSignKey(token)
	}

	cfg := agentmcp.Config{
		Version:         Version,
		DefaultImage:    *image,
		HTTPMode:        httpMode,
		ArtifactSignKey: signKey,
	}
	srv := agentmcp.NewServer(cfg)

	if httpMode {
		if token == "" {
			fmt.Fprintf(os.Stderr,
				"social-agent mcp: WARNING — no MCP_AUTH_TOKEN set, every request accepted unauthenticated.\n"+
					"  Loopback-only addrs are fine; set MCP_AUTH_TOKEN before exposing on a routable interface.\n")
		} else {
			fmt.Fprintf(os.Stderr, "social-agent mcp: bearer-token auth enabled (MCP_AUTH_TOKEN env)\n")
		}
		fmt.Fprintf(os.Stderr, "social-agent mcp: listening on %s (Streamable HTTP)\n", addr)
		fmt.Fprintf(os.Stderr, "social-agent mcp: artifacts available at %s<session_id>/<path> (HMAC-signed URLs OR same bearer token)\n", agentmcp.ArtifactsURLPrefix)
		// Register the artifacts file-server alongside /mcp on
		// the same listen addr. Goes via UnauthExtraHandlers
		// (NOT bearer-wrapped) because the handler does its own
		// dual auth — signed-URL OR bearer — to allow the
		// list_artifacts response URLs to be fetched without
		// header gymnastics while still rejecting unauthorised
		// callers.
		return mcphttp.Serve(addr, srv, mcphttp.Options{
			Service: "social-agent-mcp",
			Version: Version,
			Token:   token,
			UnauthExtraHandlers: map[string]http.Handler{
				agentmcp.ArtifactsURLPrefix: agentmcp.NewArtifactsHandler(token, signKey),
			},
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
