package main

// `social-daytona build` — refresh the local social-skills container
// image. Thin wrapper over `make docker-build` so the operator
// gets the same multi-stage build the docker-compose path uses,
// without having to remember the make target.

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmd := exec.Command("make", "docker-build")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("make docker-build: %w", err)
	}
	return nil
}
