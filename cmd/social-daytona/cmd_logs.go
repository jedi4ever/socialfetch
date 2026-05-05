package main

// `social-daytona logs <id>` — exec into a sandbox and tail the
// combined daemon logs (headless, ledger, MCP HTTP). Delegates to
// `daytona ssh`/`daytona exec` since exec-into-sandbox is a
// streaming concern and the official CLI already handles the
// websocket plumbing.
//
// We don't model the streaming ourselves — operators wanting a
// rich log UI use `social-daytona ls -f json` to pick an id, then
// `daytona ssh <id>` directly. This wrapper is the convenience
// path for "just show me what's happening in box X right now."

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "follow (tail -f); exit when omitted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("logs: <sandbox-id> required (use `social-daytona ls` to find one)")
	}
	id := fs.Arg(0)

	tailFlags := "-n 200"
	if *follow {
		tailFlags = "-n 200 -f"
	}
	// The container writes daemon logs to stderr by default — no
	// log files inside the container. So `logs` runs `dmesg`-style
	// via `journalctl --user -f`? Not available in slim debian.
	// Simplest: read /proc/<pids>/fd/2 ... too fragile.
	//
	// Pragmatic: have the container's entrypoint also tee the
	// three daemons' output into /data/logs/<svc>.log so
	// `tail -f /data/logs/*.log` works. For v1 we just point at
	// what's there; if the file doesn't exist the tail prints a
	// clear error and the operator falls back to `daytona ssh`.
	// /tmp/social-skills.log is where bootDaemons in cmd_up.go
	// redirects the entrypoint output. tail -f survives the
	// daytona-exec session ending so the agent gets a live stream
	// when --follow is passed.
	remoteCmd := fmt.Sprintf("tail %s /tmp/social-skills.log 2>/dev/null || echo '(no /tmp/social-skills.log yet — try `daytona ssh %s` for a live shell)'", tailFlags, id)

	cmd := exec.Command("daytona", "exec", id, "--", "/bin/sh", "-c", remoteCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("daytona exec %s: %w", id, err)
	}
	return nil
}
