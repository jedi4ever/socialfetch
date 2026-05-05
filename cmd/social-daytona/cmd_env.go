package main

// `social-daytona env <id>` — print export statements that point
// the local social-fetch / social-ledger CLIs at the remote
// daemons inside a Daytona sandbox. Operator does:
//
//	eval "$(social-daytona env <sandbox-id>)"
//	social-fetch fetch <url>     # uses remote chromedp + ledger
//
// And every subsequent fetch goes through the sandbox's daemon
// pool over the Daytona tunnel — so chromium runs in the sandbox,
// the local machine does only the JSON-RPC marshalling.
//
// Without this, operators have to hand-assemble four env vars
// from `up`'s output + a follow-up `daytona preview-url ID --port
// 5557` for the ledger token. Tedious; this collapses to one line.

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/jedi4ever/social-skills/internal/daytona"
)

func cmdEnv(args []string) error {
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		// No id: pick the first social-daytona-tagged sandbox.
		// Convenient for single-instance setups; ambiguous for
		// fleets, in which case we ask the operator to pin one.
		c, err := daytona.New()
		if err != nil {
			return err
		}
		ws, err := c.ListWorkspaces(context.Background())
		if err != nil {
			return err
		}
		var ours []daytona.Workspace
		for _, w := range ws {
			if w.Labels[labelKey] == "true" {
				ours = append(ours, w)
			}
		}
		switch len(ours) {
		case 0:
			return fmt.Errorf("env: no social-daytona sandboxes — run `social-daytona up -n N` first")
		case 1:
			return printEnv(c, ours[0].ID)
		default:
			return fmt.Errorf("env: %d social-daytona sandboxes — pass an id (see `social-daytona ls`)", len(ours))
		}
	}
	c, err := daytona.New()
	if err != nil {
		return err
	}
	return printEnv(c, fs.Arg(0))
}

// printEnv fetches a preview URL + token for ports 5556 (headless)
// and 5557 (ledger) and emits the export lines social-fetch /
// social-ledger consume on their next invocation. Stdout is
// shell-evalable; stderr carries the human-friendly comments.
func printEnv(c *daytona.Client, sandboxID string) error {
	ctx := context.Background()

	headless, err := c.GetPreviewURL(ctx, sandboxID, 5556, 0)
	if err != nil {
		return fmt.Errorf("preview-url 5556: %w", err)
	}
	ledger, err := c.GetPreviewURL(ctx, sandboxID, 5557, 0)
	if err != nil {
		return fmt.Errorf("preview-url 5557: %w", err)
	}

	fmt.Printf("# social-daytona env for sandbox %s\n", sandboxID)
	fmt.Printf("# eval \"$(social-daytona env %s)\" then run social-fetch / social-ledger as usual.\n", sandboxID)
	fmt.Println()
	fmt.Printf("export SOCIAL_FETCH_HEADLESS_DAEMON_URL=%q\n", headless.URL)
	fmt.Printf("export SOCIAL_FETCH_HEADLESS_DAEMON_TOKEN=%q\n", headless.Token)
	fmt.Printf("export SOCIAL_LEDGER_DAEMON_URL=%q\n", ledger.URL)
	fmt.Printf("export SOCIAL_LEDGER_DAEMON_TOKEN=%q\n", ledger.Token)
	fmt.Println()
	fmt.Println("# To unset later:")
	fmt.Println("# unset SOCIAL_FETCH_HEADLESS_DAEMON_URL SOCIAL_FETCH_HEADLESS_DAEMON_TOKEN SOCIAL_LEDGER_DAEMON_URL SOCIAL_LEDGER_DAEMON_TOKEN")

	// Hint also goes to stderr so `eval` doesn't choke on it.
	_ = strings.TrimSpace
	return nil
}
