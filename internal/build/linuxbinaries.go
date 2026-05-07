// Package build holds shared host-side build helpers used by both
// social-browser and social-agent for producing the linux/<arch>
// binaries the docker images COPY in. Lives in internal/build (not
// inside either binary's package) so the cross-compile contract is
// one definition rather than two near-copies that drift.
package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Binaries is the set of social-skills binaries the docker images
// bundle. Single source of truth for both image flavours
// (social-skills-browser:* and social-skills-agent:*) — both
// images include the full set so an in-container claude-code
// session can shell out to any of them, and the browser image
// can host social-agent itself if the operator ever wants the
// agent and pool colocated.
var Binaries = []string{"social-fetch", "social-ledger", "social-browser", "social-agent", "social-notifier"}

// LinuxBinaries cross-compiles every entry in Binaries for
// linux/<arch> into dist/linux-<arch>/. Mirrors the Makefile
// linux-binaries-<arch> target so callers without make on PATH
// (the binaries' own `provider <name> build` subcommands, mainly)
// produce the same artifact tree.
//
// CGO is disabled so the binaries are statically linked and
// reproducible across hosts — same behaviour the now-retired
// in-docker builder stage had.
func LinuxBinaries(arch string) error {
	outDir := filepath.Join("dist", "linux-"+arch)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, bin := range Binaries {
		target := filepath.Join(outDir, bin)
		fmt.Fprintf(os.Stderr, "  cross-compiling linux/%s/%s\n", arch, bin)
		c := exec.Command("go", "build",
			"-ldflags=-s -w",
			"-trimpath",
			"-o", target,
			"./cmd/"+bin,
		)
		c.Stdout = os.Stderr
		c.Stderr = os.Stderr
		c.Env = append(os.Environ(),
			"GOOS=linux",
			"GOARCH="+arch,
			"CGO_ENABLED=0",
		)
		if err := c.Run(); err != nil {
			return fmt.Errorf("build %s: %w", bin, err)
		}
	}
	return nil
}
