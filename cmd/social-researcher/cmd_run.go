package main

// cmd_run.go — the `run` subcommand. Builds a `docker run -it` invocation
// with the operator's cwd bind-mounted, env passed through, and either
// /bin/bash or `claude --mcp-config <ledger+agent>` as the in-container
// command. Replaces the current process with docker via syscall.Exec so
// the operator's terminal binds directly to the container — Ctrl-C goes
// straight to docker, no extra wrapper to leak file descriptors.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/jedi4ever/social-skills/internal/agent"
)

// envFlags is a flag.Value collecting --env KEY=VAL into a slice
// the caller can merge into the env map. Repeatable.
type envFlags []string

func (e *envFlags) String() string     { return strings.Join(*e, ",") }
func (e *envFlags) Set(v string) error { *e = append(*e, v); return nil }

// cmdRun parses the run subcommand's flags, builds the docker argv, and
// execs docker. The current process becomes docker — when docker exits,
// we exit with its status.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	workdir := fs.String("workdir", "", "host path bind-mounted at /workspace (default: cwd)")
	image := fs.String("image", "social-skills-agent:"+Version, "docker image to run")
	useClaude := fs.Bool("claude", false, "start `claude` TUI with --mcp-config (ledger + agent) instead of /bin/bash")
	noDockerSock := fs.Bool("no-docker-sock", false, "don't bind-mount /var/run/docker.sock (only relevant with --claude)")
	name := fs.String("name", "", "explicit container name (default: auto-generated)")
	var extraEnv envFlags
	fs.Var(&extraEnv, "env", "add an env var (KEY=VAL). Repeatable. Merged on top of host PassthroughKeys.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Resolve workdir: explicit flag wins, otherwise cwd.
	if *workdir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		*workdir = cwd
	}

	// Build env: PassthroughKeys from host env first, then operator's
	// --env entries (which win on collision). Loopback rewrite applies
	// to every value so SOCIAL_LEDGER_DAEMON_URL=http://127.0.0.1:5557
	// becomes http://host.docker.internal:5557 inside the container.
	hostEnv := parseEnviron(os.Environ())
	envMap := agent.BuildPassthroughEnv(hostEnv)
	for _, kv := range extraEnv {
		k, v, ok := splitKV(kv)
		if !ok {
			return fmt.Errorf("--env %q: expected KEY=VAL", kv)
		}
		envMap[k] = v
	}

	// Resolve docker binary. We shell out (rather than dialing the
	// socket directly) for the same reason the docker provider does:
	// `docker run -it` handles TTY plumbing and signal forwarding far
	// better than re-implementing it on top of the engine API.
	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker not found on PATH: %w", err)
	}

	argv := []string{
		"docker", "run", "--rm", "-it",
		"--label", "social-researcher=true",
		"--add-host", "host.docker.internal:host-gateway",
		"-w", "/workspace",
		"-v", *workdir + ":/workspace",
	}
	if *name != "" {
		argv = append(argv, "--name", *name)
	}
	if *useClaude && !*noDockerSock {
		// docker.sock mount lets the inner social-agent MCP shell
		// out to docker. Without it, mcp__agent__social_agent_run
		// fails — the agent surface still registers but every call
		// errors at exec time.
		argv = append(argv, "-v", "/var/run/docker.sock:/var/run/docker.sock")
	}
	for k, v := range envMap {
		argv = append(argv, "-e", k+"="+agent.RewriteLoopbackURL(v))
	}
	argv = append(argv, *image)

	if *useClaude {
		mcpConfig, err := buildClaudeMCPConfig()
		if err != nil {
			return fmt.Errorf("build mcp config: %w", err)
		}
		argv = append(argv, "claude",
			"--dangerously-skip-permissions",
			"--mcp-config", mcpConfig,
		)
	} else {
		argv = append(argv, "/bin/bash")
	}

	// Replace the current process with docker. The operator's tty
	// binds directly to the container; signals + exit codes propagate
	// without an extra layer.
	if err := syscall.Exec(dockerBin, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec docker: %w", err)
	}
	return nil // unreachable on successful Exec
}

// buildClaudeMCPConfig returns the inline JSON `claude --mcp-config`
// expects, registering two stdio MCP servers inside the container:
// social-ledger mcp and social-agent mcp. Both binaries live at
// /usr/local/bin in the agent image.
func buildClaudeMCPConfig() (string, error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"ledger": map[string]any{
				"command": "/usr/local/bin/social-ledger",
				"args":    []string{"mcp"},
			},
			"agent": map[string]any{
				"command": "/usr/local/bin/social-agent",
				"args":    []string{"mcp"},
			},
		},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// parseEnviron splits an os.Environ() slice into a map. Stops at the
// first '=' so values can contain '='. Empty entries are skipped.
func parseEnviron(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, ok := splitKV(kv)
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}

// splitKV returns (key, value, ok). Splits on the first '='.
func splitKV(s string) (string, string, bool) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
