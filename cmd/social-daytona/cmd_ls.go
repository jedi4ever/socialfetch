package main

// `social-daytona ls` — list every sandbox tagged with our
// `social-daytona=true` label. Filtered client-side because the
// Daytona /workspace endpoint doesn't accept label filters at
// request time today.
//
// Output format: human-readable table by default, JSON via
// `-f json` for piping into other tools (e.g. `social-daytona ls
// -f json | jq -r '.[].id' | xargs social-daytona logs`).

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jedi4ever/social-skills/internal/daytona"
)

const labelKey = "social-daytona"

func cmdLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	format := fs.String("f", "text", "output format: text (default) | json")
	all := fs.Bool("all", false, "include sandboxes NOT created by social-daytona (raw list)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	c, err := daytona.New()
	if err != nil {
		return err
	}
	ctx := context.Background()
	rows, err := c.ListWorkspaces(ctx)
	if err != nil {
		return err
	}

	out := make([]daytona.Workspace, 0, len(rows))
	for _, w := range rows {
		if *all || w.Labels[labelKey] == "true" {
			out = append(out, w)
		}
	}

	if *format == "json" {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if len(out) == 0 {
		if *all {
			fmt.Println("(no sandboxes)")
		} else {
			fmt.Println("(no social-daytona sandboxes — try `social-daytona up -n N` or pass --all to see everything)")
		}
		return nil
	}
	// Two-column-ish table; widths chosen so a 38-char UUID fits
	// without wrapping in a 120-col terminal.
	for _, w := range out {
		instance := w.Labels["social-daytona-instance"]
		if instance == "" {
			instance = "-"
		}
		state := strings.ToLower(w.State)
		if state == "" {
			state = "?"
		}
		fmt.Printf("%-40s  %-10s  inst=%-3s  %s\n", w.ID, state, instance, w.Snapshot)
	}
	return nil
}
