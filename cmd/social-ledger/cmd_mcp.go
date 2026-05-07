package main

// social-ledger mcp — ledger-only MCP server.
//
// Mirrors `social-fetch mcp`'s wiring but registers only the
// social_ledger_* tool family (plus social_fetch_read_file, which
// pages through the temp files social_ledger_get produces). Aimed
// at third-party agents whose job is purely "read what's been
// seen, record what I just learned" and shouldn't have outbound
// HTTP capability.
//
// Two transports:
//
//   stdio (default)   default; what .mcp.json registers in
//                     Claude Desktop / Claude Code.
//
//   --http :PORT      Streamable HTTP. Used by an inner claude
//                     running inside a social-researcher container
//                     — it points at the host's
//                     http://host.docker.internal:PORT/mcp and
//                     queries the host's ledger DB without needing
//                     the binary or DB inside the container.
//
// Daemon-routing is automatic — the same SOCIAL_LEDGER_DAEMON_URL
// the social-fetch path uses. Set SOCIAL_LEDGER_READONLY=1 to flip
// the write tools (record / forget) into refused mode while keeping
// the read surface live. MCP_AUTH_TOKEN gates /mcp on the HTTP path.

import (
	"flag"
	"fmt"
	"os"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/mcp"
	"github.com/jedi4ever/social-skills/internal/util/mcphttp"
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	readonly := fs.Bool("readonly", false, "set SOCIAL_LEDGER_READONLY=1 for this process so record/forget refuse")
	httpAddr := fs.String("http", "", "if set, serve over Streamable HTTP on this bind addr instead of stdio. Bare port (5557) and host-less colon-port (:5557) both bind on all interfaces — the form to use when a containerised inner claude on host.docker.internal needs to reach you. Pass an explicit host (127.0.0.1:5557) to lock to loopback. MCP_AUTH_TOKEN env adds bearer auth.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *readonly {
		// Set in this process only — exporting via flag is a
		// shorthand so operators don't have to remember the env
		// var name. Tool handlers read the env var on every call.
		_ = os.Setenv("SOCIAL_LEDGER_READONLY", "1")
	}

	// Point the subprocess fallback at our own executable. Most
	// ledger ops daemon-route via SOCIAL_LEDGER_DAEMON_URL (no
	// binary needed), but a few (like article record with its
	// JSONL-stdin flow) still shell out. Without this the
	// fallback hits "social-ledger not on $PATH" because the
	// MCP server's process inherits Claude Code's env, not the
	// dist/ directory. os.Executable() resolves to whatever
	// invoked us — exactly the binary the subprocess wants.
	if os.Getenv("SOCIAL_LEDGER_BIN") == "" {
		if exe, err := os.Executable(); err == nil {
			_ = os.Setenv("SOCIAL_LEDGER_BIN", exe)
		}
	}

	cfg := mcp.Config{Version: Version}
	srv := mcp.NewLedgerOnlyServer(cfg)

	addr := strings.TrimSpace(*httpAddr)
	if addr != "" {
		// Bare port shortcut — `--http 5557` becomes `:5557`
		// (all-interfaces bind), matching what's needed for a
		// containerised inner claude on host.docker.internal to
		// reach us. Explicit host:port (127.0.0.1:5557) passes
		// through unchanged.
		if !strings.Contains(addr, ":") {
			addr = ":" + addr
		}
		token := strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN"))
		if token == "" {
			fmt.Fprintf(os.Stderr,
				"social-ledger mcp: WARNING — no MCP_AUTH_TOKEN set, every request accepted unauthenticated.\n"+
					"  Loopback-only addrs are fine; set MCP_AUTH_TOKEN before exposing on a routable interface.\n")
		} else {
			fmt.Fprintf(os.Stderr, "social-ledger mcp: bearer-token auth enabled (MCP_AUTH_TOKEN env)\n")
		}
		fmt.Fprintf(os.Stderr, "social-ledger mcp: listening on %s (Streamable HTTP)\n", addr)
		return mcphttp.Serve(addr, srv, mcphttp.Options{
			Service: "social-ledger-mcp",
			Version: Version,
			Token:   token,
		})
	}

	if err := mcpgo.ServeStdio(srv); err != nil {
		return fmt.Errorf("serve stdio: %w", err)
	}
	return nil
}
