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
	"runtime"
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
	// Default image is `social-skills-researcher:latest` — built by
	// `make researcher-build` (a thin re-tag of social-skills-agent
	// today). Floating tag, not version-pinned, so a host-binary
	// version bump doesn't force a per-version image rebuild.
	// Operators wanting a specific tag pass --image explicitly.
	image := fs.String("image", "social-skills-researcher:latest", "docker image to run")
	useClaude := fs.Bool("claude", false, "start `claude` TUI with --mcp-config (ledger + agent) instead of /bin/bash")
	noDockerSock := fs.Bool("no-docker-sock", false, "don't bind-mount /var/run/docker.sock (only relevant with --claude)")
	agentMCPURL := fs.String("agent-mcp-url", "", "register the agent MCP via Streamable HTTP at this URL. Empty = layered default: $SOCIAL_AGENT_MCP_URL, then http://host.docker.internal:5562/mcp. Bearer auth via MCP_AUTH_TOKEN. Pass --stdio to bypass HTTP entirely.")
	ledgerMCPURL := fs.String("ledger-mcp-url", "", "register the ledger MCP via Streamable HTTP at this URL. Empty = layered default: $SOCIAL_LEDGER_MCP_URL, then http://host.docker.internal:5557/mcp. Bearer auth via MCP_AUTH_TOKEN. Pass --stdio to bypass HTTP entirely.")
	stdio := fs.Bool("stdio", false, "force stdio MCP for both servers — inner claude spawns /usr/local/bin/social-{agent,ledger} mcp inside the container instead of dialing host HTTP. Use when you don't have host MCP servers running. Implies the docker.sock mount unless --no-docker-sock.")
	name := fs.String("name", "", "explicit container name (default: auto-generated)")
	var extraEnv envFlags
	fs.Var(&extraEnv, "env", "add an env var (KEY=VAL). Repeatable. Merged on top of host PassthroughKeys.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Resolve MCP URLs with layered defaults so the operator's
	// `social-researcher run --claude` works zero-config in the common
	// topology (host runs `social-{agent,ledger} mcp --http`):
	//
	//   1. Explicit --*-mcp-url flag wins.
	//   2. Else, $SOCIAL_{AGENT,LEDGER}_MCP_URL env wins.
	//   3. Else, the canonical localhost ports (5562 / 5557) at
	//      host.docker.internal — what `social-{agent,ledger} mcp --http`
	//      defaults to. Operators only override when they've moved the
	//      servers to a different host or port.
	//
	// --stdio overrides everything — wipes both URLs so the inner claude
	// falls back to spawning the binaries inside the container. Useful
	// when no host HTTP servers exist (e.g. quick smoke test).
	if *agentMCPURL == "" {
		*agentMCPURL = strings.TrimSpace(os.Getenv("SOCIAL_AGENT_MCP_URL"))
	}
	if *agentMCPURL == "" {
		*agentMCPURL = "http://host.docker.internal:5562/mcp"
	}
	if *ledgerMCPURL == "" {
		*ledgerMCPURL = strings.TrimSpace(os.Getenv("SOCIAL_LEDGER_MCP_URL"))
	}
	if *ledgerMCPURL == "" {
		*ledgerMCPURL = "http://host.docker.internal:5557/mcp"
	}
	if *stdio {
		*agentMCPURL = ""
		*ledgerMCPURL = ""
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
		"-w", "/workspace",
		"-v", *workdir + ":/workspace",
	}
	// On Linux native Docker, host.docker.internal isn't resolvable by
	// default — `--add-host host.docker.internal:host-gateway` is the
	// usual fix. On macOS Docker Desktop, however, host.docker.internal
	// already resolves to a routable IP that bridges to host loopback;
	// adding --add-host there pins it to the docker0 bridge gateway
	// (172.17.0.1) instead, which can NOT reach host loopback. So we
	// only emit --add-host on Linux. Detect "is this Docker Desktop?"
	// is awkward; use GOOS as the proxy — accurate in practice because
	// macOS = Docker Desktop, Linux = native Docker (or Colima, which
	// also benefits from --add-host).
	if runtime.GOOS == "linux" {
		argv = append(argv, "--add-host", "host.docker.internal:host-gateway")
	}
	if *name != "" {
		argv = append(argv, "--name", *name)
	}
	// docker.sock mount only matters when the inner claude is
	// expected to spawn sibling containers via the in-container
	// social-agent CLI. When --agent-mcp-url is set, the inner
	// claude calls the *host's* social-agent MCP over HTTP instead
	// — that's what the user asked for; no socket needed. Default
	// path (no --agent-mcp-url, no --no-docker-sock) keeps the
	// existing socket-mount behaviour for backward compat.
	mountDockerSock := *useClaude && !*noDockerSock && *agentMCPURL == ""
	if mountDockerSock {
		argv = append(argv, "-v", "/var/run/docker.sock:/var/run/docker.sock")
	}
	for k, v := range envMap {
		argv = append(argv, "-e", k+"="+agent.RewriteLoopbackURL(v))
	}
	argv = append(argv, *image)

	if *useClaude {
		token := strings.TrimSpace(envMap["MCP_AUTH_TOKEN"])
		mcpConfig, err := buildClaudeMCPConfig(*agentMCPURL, *ledgerMCPURL, token)
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
// expects. Each registered server is one of two shapes:
//
//   - stdio (default): {"command":"/usr/local/bin/social-ledger",
//     "args":["mcp"]} — claude spawns the binary
//     inside the container and pipes JSON-RPC.
//   - http: {"type":"http","url":"<url>","headers":{...}} — claude
//     opens an HTTP/SSE connection to the supplied
//     URL. Used when the operator points at a
//     host-running `social-agent mcp --http` /
//     `social-ledger mcp --http`, so the inner
//     claude controls the host's resources without
//     needing the binary or docker socket inside
//     the container.
//
// agentURL / ledgerURL select per-server: empty = stdio, non-empty =
// http. token, when non-empty, becomes the Authorization header for
// every HTTP entry — same value the corresponding host server
// validates via MCP_AUTH_TOKEN.
func buildClaudeMCPConfig(agentURL, ledgerURL, token string) (string, error) {
	servers := map[string]any{
		"ledger": mcpEntry("/usr/local/bin/social-ledger", ledgerURL, token),
		"agent":  mcpEntry("/usr/local/bin/social-agent", agentURL, token),
	}
	body, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// mcpEntry returns the stdio-spawn entry when url is empty,
// otherwise the HTTP entry. binPath is the in-container binary path
// for the stdio fallback (unused when url is set).
func mcpEntry(binPath, url, token string) map[string]any {
	if url == "" {
		return map[string]any{
			"command": binPath,
			"args":    []string{"mcp"},
		}
	}
	entry := map[string]any{
		"type": "http",
		"url":  url,
	}
	if token != "" {
		entry["headers"] = map[string]any{
			"Authorization": "Bearer " + token,
		}
	}
	return entry
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
