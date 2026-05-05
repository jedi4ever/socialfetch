package main

// `social-daytona push` — upload the local social-skills:<version>
// image to Daytona as a snapshot. Delegates to `daytona snapshot
// push` because the multipart upload + image-spec dance is
// non-trivial and the official CLI already implements it.
//
// Snapshot name defaults to `social-skills:<version>` (matches the
// Docker tag for the running binary), so `social-daytona up`
// can find it without the operator passing a name explicitly.
//
// Docker socket auto-detection: the daytona CLI uses Docker's
// default socket path (/var/run/docker.sock), but on macOS the
// active socket depends on which docker runtime is installed —
// Docker Desktop, Rancher Desktop, OrbStack, Colima all put the
// socket somewhere different. We probe `docker context inspect`
// for the active context's endpoint and pass it through DOCKER_HOST
// when DOCKER_HOST isn't already set, so `social-daytona push`
// just works without operators having to remember to export it.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	// Two env nudges:
	//   - DOCKER_HOST: rancher-desktop / orbstack put the docker
	//     socket outside the default /var/run/docker.sock path.
	//   - DAYTONA_API_URL: required for the CLI's API-key auth
	//     to actually authenticate; absent it returns 401 even
	//     with a valid DAYTONA_API_KEY in env.
	cmd.Env = ensureDaytonaAPIEnv(ensureDockerHost(os.Environ()))
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

// ensureDaytonaAPIEnv adds DAYTONA_API_URL when it's missing —
// the daytona CLI's API-key auth path silently rejects credentials
// when DAYTONA_API_URL is empty (it doesn't fall through to its
// own embedded default). Set it to https://app.daytona.io/api so
// API-key auth works without the operator having to add the var
// to .env. DAYTONA_API_KEY + DAYTONA_ORG_ID propagate verbatim
// when already set.
func ensureDaytonaAPIEnv(env []string) []string {
	hasURL := false
	for _, v := range env {
		if strings.HasPrefix(v, "DAYTONA_API_URL=") && len(v) > len("DAYTONA_API_URL=") {
			hasURL = true
			break
		}
	}
	if !hasURL {
		env = append(env, "DAYTONA_API_URL=https://app.daytona.io/api")
	}
	return env
}

// ensureDockerHost augments env with a working DOCKER_HOST when
// none is set. The daytona CLI talks to Docker via the standard
// SDK which only checks DOCKER_HOST + the hard-coded
// /var/run/docker.sock fallback — neither of which works out of
// the box on macOS with Rancher Desktop / OrbStack / Colima
// (they put the socket under the user's home).
//
// Strategy:
//
//  1. If DOCKER_HOST already set, leave it alone — operator's
//     configuration wins.
//  2. Otherwise ask `docker context inspect <active>` for the
//     active context's endpoint and use that. The user has
//     already chosen which runtime they're using when they
//     `docker context use rancher-desktop`; we just propagate
//     that to the daytona subprocess.
//  3. If the docker CLI isn't on PATH or the inspect fails,
//     return env unchanged — daytona will hit the default path,
//     fail, and the operator gets the same error they'd see if
//     the docker daemon really were down.
func ensureDockerHost(env []string) []string {
	for _, v := range env {
		if strings.HasPrefix(v, "DOCKER_HOST=") && len(v) > len("DOCKER_HOST=") {
			return env
		}
	}
	host := activeDockerHost()
	if host == "" {
		return env
	}
	return append(env, "DOCKER_HOST="+host)
}

// activeDockerHost returns the endpoint of the user's currently
// selected docker context, or "" when something fails. Two-step
// lookup: `docker context show` for the name, `docker context
// inspect <name>` for the endpoint. We don't use --format because
// older CLIs render endpoint differently; the JSON shape is
// stable across versions.
func activeDockerHost() string {
	// Step 1 — current context name.
	out, err := exec.Command("docker", "context", "show").Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return ""
	}
	// Step 2 — JSON inspection for the endpoint.
	out, err = exec.Command("docker", "context", "inspect", name).Output()
	if err != nil {
		return ""
	}
	var ctxs []struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(out, &ctxs); err != nil || len(ctxs) == 0 {
		return ""
	}
	if d, ok := ctxs[0].Endpoints["docker"]; ok && d.Host != "" {
		return d.Host
	}
	return ""
}
