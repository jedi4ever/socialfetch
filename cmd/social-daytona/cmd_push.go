package main

// `social-daytona push` — upload the local social-skills:<version>
// image to Daytona as a snapshot. Delegates to `daytona snapshot
// push` because the multipart upload + image-spec dance is
// non-trivial and the official CLI already implements it.
//
// Snapshot name defaults to `social-skills:<version>` (matches the
// Docker tag for the running binary), so `social-daytona up`
// can find it without the operator passing a name explicitly.

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	tag := fs.String("tag", "social-skills:"+Version, "docker image tag to push (default: social-skills:<social-daytona version>)")
	name := fs.String("name", "", "snapshot name to register on Daytona (default: same as --tag)")
	cpu := fs.Int("cpu", 2, "CPU cores allocated to sandboxes built from this snapshot")
	memory := fs.Int("memory", 2, "memory in GB for sandboxes built from this snapshot")
	disk := fs.Int("disk", 3, "disk in GB for sandboxes built from this snapshot")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		*name = *tag
	}

	// `daytona snapshot push <tag> --name <name> --cpu N --memory N --disk N`
	cmdArgs := []string{
		"snapshot", "push", *tag,
		"--name", *name,
		"--cpu", itoaInt(*cpu),
		"--memory", itoaInt(*memory),
		"--disk", itoaInt(*disk),
	}
	cmd := exec.Command("daytona", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ() // pass DAYTONA_API_KEY etc through
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("daytona snapshot push: %w", err)
	}
	fmt.Fprintf(os.Stderr, "pushed %s as snapshot %s\n", *tag, *name)
	return nil
}

// itoaInt is a tiny shim so we don't pull strconv into every file.
func itoaInt(n int) string {
	return fmt.Sprintf("%d", n)
}
