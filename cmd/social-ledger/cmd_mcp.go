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
// Daemon-routing is automatic — the same SOCIAL_LEDGER_DAEMON_URL
// the social-fetch path uses. Set SOCIAL_LEDGER_READONLY=1 to flip
// the write tools (record / forget) into refused mode while keeping
// the read surface live.

import (
	"flag"
	"fmt"
	"os"

	mcpgo "github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/mcp"
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	readonly := fs.Bool("readonly", false, "set SOCIAL_LEDGER_READONLY=1 for this process so record/forget refuse")
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

	// Stdio is the only transport for v1 — same as social-fetch
	// mcp / social-agent ask-mcp. HTTP/ngrok comes later if a
	// hosted use case demands it.
	if err := mcpgo.ServeStdio(srv); err != nil {
		return fmt.Errorf("serve stdio: %w", err)
	}
	return nil
}
