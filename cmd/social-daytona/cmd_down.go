package main

// `social-daytona down [<id>...]` — tear down sandboxes.
//
// No args = delete every sandbox carrying our `social-daytona=true`
// label. With args = delete only those ids. Either way we hit the
// API in series; concurrent deletes don't speed things up
// noticeably and serial output is easier to follow.

import (
	"context"
	"flag"
	"fmt"

	"github.com/jedi4ever/social-skills/internal/daytona"
)

func cmdDown(args []string) error {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	c, err := daytona.New()
	if err != nil {
		return err
	}
	ctx := context.Background()

	ids := fs.Args()
	if len(ids) == 0 {
		// Default: delete every social-daytona-tagged sandbox.
		all, err := c.ListWorkspaces(ctx)
		if err != nil {
			return err
		}
		for _, w := range all {
			if w.Labels[labelKey] == "true" {
				ids = append(ids, w.ID)
			}
		}
		if len(ids) == 0 {
			fmt.Println("(no social-daytona sandboxes to delete)")
			return nil
		}
	}

	for _, id := range ids {
		if err := c.DeleteWorkspace(ctx, id); err != nil {
			fmt.Printf("delete %s FAILED: %v\n", id, err)
			continue
		}
		fmt.Printf("deleted %s\n", id)
	}
	return nil
}
