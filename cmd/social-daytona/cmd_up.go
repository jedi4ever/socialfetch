package main

// `social-daytona up -n N` — create N sandboxes from the
// social-skills snapshot, fetch a per-instance preview URL for
// port 5558 (MCP), print a table the operator can paste into a
// Claude Desktop / claude.ai connector config.
//
// Each sandbox is tagged with three labels so `ls` / `down` can
// find them without the operator tracking ids:
//
//   social-daytona            = true        (our marker)
//   social-daytona-instance   = <0..N-1>    (which instance)
//   social-daytona-version    = <version>   (which release of us
//                                            launched it)
//
// Tunneling: per-instance via Daytona's preview-url. Each sandbox
// gets its own signed URL pointing at port 5558, default 1h
// expiration (override with --expires).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/daytona"
)

func cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	n := fs.Int("n", 1, "number of sandboxes to spin up")
	snapshot := fs.String("snapshot", "social-skills:"+Version, "snapshot name to launch from (default: social-skills:<this version>)")
	cpu := fs.Int("cpu", 2, "CPU cores per sandbox")
	memory := fs.Int("memory", 2, "memory per sandbox in GB")
	disk := fs.Int("disk", 3, "disk per sandbox in GB")
	target := fs.String("target", "", "target region (eu, us); empty = org default")
	authToken := fs.String("token", "", "MCP_AUTH_TOKEN to bake into each sandbox; empty = auto-generate one (shared across the batch)")
	expires := fs.Int("expires", 3600, "preview URL expiration in seconds")
	port := fs.Int("port", 5558, "port to expose via the preview URL (default 5558 = MCP HTTP)")
	autoStop := fs.Int("auto-stop", 0, "auto-stop after N minutes of inactivity (0 = never auto-stop, runs until `social-daytona down`). Daytona's own default would be 15min which is too short for most dev sessions.")
	autoArchive := fs.Int("auto-archive", 0, "auto-archive a stopped sandbox after N minutes (0 = use Daytona default of ~7 days)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *n < 1 {
		return fmt.Errorf("up: -n must be >= 1")
	}

	c, err := daytona.New()
	if err != nil {
		return err
	}
	ctx := context.Background()

	// Auto-generate one shared token when none provided. Sharing
	// across the batch keeps the operator's connector config
	// short — same token for all N URLs. Pass --token to use a
	// pre-assigned one (e.g. when wiring through a vault).
	token := strings.TrimSpace(*authToken)
	if token == "" {
		token = randomHex(32)
		fmt.Fprintf(os.Stderr, "auto-generated MCP_AUTH_TOKEN (shared): %s\n\n", token)
	}

	// Print a header so the URL list is easy to spot in a
	// terminal scrollback.
	fmt.Fprintf(os.Stderr, "spawning %d sandbox(es) from %s ...\n", *n, *snapshot)

	type result struct {
		ID  string
		URL string
		Err error
	}
	results := make([]result, *n)
	for i := 0; i < *n; i++ {
		req := daytona.CreateWorkspaceRequest{
			Image:  *snapshot, // API field is `image`, not `snapshot`
			CPU:    *cpu,
			Memory: *memory,
			Disk:   *disk,
			Target: *target,
			Env: map[string]string{
				"MCP_AUTH_TOKEN": token,
			},
			Labels: map[string]string{
				labelKey:                  "true",
				"social-daytona-instance": fmt.Sprintf("%d", i),
				"social-daytona-version":  Version,
			},
			AutoStopInterval: intPtr(*autoStop),
		}
		if *autoArchive > 0 {
			req.AutoArchiveInterval = intPtr(*autoArchive)
		}
		ws, err := c.CreateWorkspace(ctx, req)
		if err != nil {
			results[i] = result{Err: err}
			continue
		}

		// Boot the daemons inside the sandbox. Daytona's runtime
		// runs its own daytona binary as PID 1 and doesn't honour
		// the image's CMD the same way `docker run` does — our
		// entrypoint script gets exec'd but exits before the
		// daemons stay listening. Instead we explicitly call
		// `daytona exec` to detach the entrypoint as a background
		// process. setsid + nohup + redirect-to-file gives us a
		// boot that survives the exec session ending, plus a
		// log file we can tail later.
		if err := bootDaemons(ws.ID); err != nil {
			results[i] = result{ID: ws.ID, Err: fmt.Errorf("boot daemons: %w", err)}
			continue
		}

		// Wait for the daemons to actually listen. Headless takes
		// the longest because of the chromium pool warmup; ledger
		// and MCP come up in a few seconds. Poll up to ~30s before
		// surfacing the preview URL — better to have a working URL
		// on first display than confuse the operator with a 502.
		waitForDaemons(ws.ID, 30*time.Second)

		preview, err := c.GetPreviewURL(ctx, ws.ID, *port, *expires)
		if err != nil {
			// Sandbox created OK but preview URL failed; report
			// the id + error so the operator can retry with
			// `social-daytona ls` + manual preview-url.
			results[i] = result{ID: ws.ID, Err: err}
			continue
		}
		results[i] = result{ID: ws.ID, URL: preview.URL}
	}

	// Pretty-print results. Each row: instance | id | URL.
	fmt.Println()
	for i, r := range results {
		switch {
		case r.Err != nil && r.ID == "":
			fmt.Printf("[%d]  CREATE FAILED: %v\n", i, r.Err)
		case r.Err != nil:
			fmt.Printf("[%d]  %s  PREVIEW FAILED: %v\n", i, r.ID, r.Err)
		default:
			fmt.Printf("[%d]  %s  %s\n", i, r.ID, r.URL)
		}
	}

	// Wrap up — token reminder + connector hint
	fmt.Println()
	fmt.Fprintf(os.Stderr, "MCP endpoint:  <url>/mcp\n")
	fmt.Fprintf(os.Stderr, "Bearer token:  %s\n", token)
	fmt.Fprintf(os.Stderr, "Tear down:     social-daytona down\n")
	return nil
}

// randomHex returns a hex-encoded string of n random bytes
// (so the resulting string is 2n chars). Used for the
// auto-generated MCP_AUTH_TOKEN when --token isn't passed.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// intPtr is a tiny helper for the auto-stop / auto-archive
// fields, which need a pointer to distinguish "send 0 to mean
// never" from "field absent, use API default."
func intPtr(n int) *int { return &n }

// bootDaemons launches docker-entrypoint.sh inside the sandbox as
// a detached background process. Daytona's runtime PID 1 is its
// own daytona binary which doesn't honour the docker image's
// CMD persistently — our entrypoint runs once but exits, leaving
// no daemons. By calling `daytona exec` after create we control
// when + how the daemons start, and we get a log file we can
// follow with `social-daytona logs`.
//
// The shell wrapper does three things in one line:
//
//  1. setsid: detach from the controlling terminal so the
//     process tree survives the exec session ending.
//  2. nohup: ignore SIGHUP, redirect output to /var/log.
//  3. < /dev/null: close stdin so the wrapping `daytona exec`
//     can return immediately without waiting for input.
//
// Without all three, daytona exec hangs (waiting for the
// foreground entrypoint) or the daemons get killed when our exec
// session disconnects.
func bootDaemons(sandboxID string) error {
	// Log file lives in /tmp because the non-root sf user can't
	// write to /var/log inside the Daytona sandbox image. /tmp is
	// world-writable per Linux convention; tail it via
	// `daytona exec <id> -- tail -f /tmp/social-skills.log`.
	wrapper := `setsid nohup /usr/local/bin/docker-entrypoint.sh all > /tmp/social-skills.log 2>&1 < /dev/null &
exit 0`
	cmd := exec.Command("daytona", "exec", sandboxID, "--", "/bin/sh", "-c", wrapper)
	cmd.Env = ensureDaytonaAPIEnv(os.Environ())
	cmd.Stdout = os.Stderr // log line goes to stderr so the URL list on stdout stays clean
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("daytona exec: %w", err)
	}
	return nil
}

// waitForDaemons polls the in-sandbox /health endpoint via
// `daytona exec curl 127.0.0.1:5558/health` until either it
// responds 200 or the timeout fires. Returns true on success.
// Side effect: lengthens the up flow by up to `timeout` seconds
// for first-time spawns; on a warm pool the call returns in <1s.
func waitForDaemons(sandboxID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("daytona", "exec", sandboxID, "--",
			"/bin/sh", "-c",
			"curl -fsS -o /dev/null -m 2 http://127.0.0.1:5558/health")
		cmd.Env = ensureDaytonaAPIEnv(os.Environ())
		// Discard output — we only care about exit code.
		if err := cmd.Run(); err == nil {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}
