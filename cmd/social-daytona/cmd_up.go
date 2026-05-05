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
		}
		ws, err := c.CreateWorkspace(ctx, req)
		if err != nil {
			results[i] = result{Err: err}
			continue
		}

		// Settle a moment so the sandbox transitions to "started"
		// before we ask for a preview URL. The API tolerates an
		// early call but the resulting URL won't connect until
		// the workspace's port is listening — better to pause and
		// have a working URL on first display than confuse the
		// operator with a 502.
		time.Sleep(500 * time.Millisecond)

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
