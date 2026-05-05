package main

// `social-daytona build` — refresh the local social-skills container
// image. Builds for `linux/amd64` regardless of the host architecture
// because Daytona's sandbox runtime is x86_64 — pushing an arm64
// image (the default on Apple Silicon) gets rejected with
// `image is not compatible with AMD architecture`.
//
// Falls back to `make docker-build` (native architecture) when the
// operator passes --native; useful when they only want to docker-run
// locally and not push to Daytona.

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	tag := fs.String("tag", "social-skills:"+Version, "docker image tag to build")
	native := fs.Bool("native", false, "build for the host's architecture (skip the linux/amd64 cross-compile). Useful when you only docker-run locally and don't push to Daytona.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cmdArgs := []string{"build"}
	if !*native {
		cmdArgs = append(cmdArgs, "--platform", "linux/amd64")
	}
	cmdArgs = append(cmdArgs, "-t", *tag, "-t", "social-skills:latest", ".")

	fmt.Fprintf(os.Stderr, "building %s ...\n", *tag)
	cmd := exec.Command("docker", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = ensureDockerHost(os.Environ())
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	return nil
}
