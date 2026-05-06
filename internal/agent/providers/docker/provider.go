// Package docker is the local-docker Provider for social-agent
// sessions. Shells out to the `docker` CLI rather than calling
// the docker SDK directly — same approach social-browser's daytona
// provider takes, and the same approach dclaude's docker provider
// uses. Smaller binary, no docker-go-sdk pin, easier debugging
// (operator can `docker ...` the same flags by hand).
package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jedi4ever/social-skills/internal/agent"
	"github.com/jedi4ever/social-skills/internal/agent/artifacts"
	"github.com/jedi4ever/social-skills/internal/agent/harness"
	"github.com/jedi4ever/social-skills/internal/agent/streaming"
)

// LabelKey is what the provider stamps on every container it
// creates. List filters by this label so List() returns only "our"
// containers, not random ones the operator launched separately.
const LabelKey = "social-agent"

// DefaultImage is the image:tag launched when UpOpts.Image is
// empty. Matches the tag `make agent-build-<arch>` produces.
const DefaultImage = "social-skills-agent:latest"

// ArtifactsContainerPort is the port the in-container
// `social-agent artifacts serve` listens on. Hard-coded matching
// the entrypoint script + Dockerfile.agent EXPOSE line. The host
// side is whatever `-p 127.0.0.1:0:5563` resolved to — read back
// via `docker port` after Up.
const ArtifactsContainerPort = 5563

// Provider is the docker substrate. Stateless beyond the docker
// daemon itself — every method shells out per-call.
type Provider struct{}

// New returns a Provider. Cheap to construct; safe to call from
// every entrypoint without caching.
func New() *Provider { return &Provider{} }

// Name identifies the provider in CLI output and Session.Provider
// stamping.
func (p *Provider) Name() string { return "docker" }

// Up creates a new agent container and returns its metadata.
// Reuses an existing one with the same UpOpts.Name when it's still
// running — `up` is idempotent on names. Streams docker's stderr
// through to the caller's stderr so a pull-on-first-use shows
// progress.
func (p *Provider) Up(ctx context.Context, opts agent.UpOpts) (*agent.Session, error) {
	hName := opts.Harness
	if hName == "" {
		hName = "claude-code"
	}
	h, err := harness.Get(hName)
	if err != nil {
		return nil, err
	}
	image := opts.Image
	if image == "" {
		image = DefaultImage
	}

	// Reuse-existing path: if a container with this name is already
	// running, return its metadata instead of failing on `docker run`.
	if opts.Name != "" {
		if s, err := p.inspect(ctx, opts.Name); err == nil && s != nil && s.State == "running" {
			s.Harness = hName
			return s, nil
		}
	}

	// Compose the docker run argv. -d keeps the container in the
	// background; --label tags it as ours; --rm is NOT set so
	// crashed containers leave a corpse the operator can `docker
	// logs` after the fact. --add-host=host.docker.internal:host-gateway
	// makes the docker-on-macOS magic name resolvable on Linux too,
	// so env values like SOCIAL_FETCH_HEADLESS_DAEMON_URL=
	// http://host.docker.internal:5560 work the same on every host.
	args := []string{"run", "-d",
		// -p 127.0.0.1:0:5563 — let docker pick a free host port
		// (the :0:) bound only on loopback (the 127.0.0.1:). Read
		// back via `docker port` after run completes.
		"-p", fmt.Sprintf("127.0.0.1:0:%d", ArtifactsContainerPort),
		"--label", LabelKey + "=true",
		"--label", LabelKey + "-harness=" + hName,
		"--label", LabelKey + "-image=" + image,
	}
	// Linux + a few other non-Desktop dockers need explicit
	// --add-host for host.docker.internal to resolve. macOS
	// Docker Desktop already auto-injects the right routing
	// hostname; adding our own override there points the name at
	// the bridge gateway (172.17.0.1) which is NOT reachable from
	// outside the VM. Skip the flag on darwin so Docker Desktop's
	// default reachable-host wiring stays intact.
	if runtime.GOOS != "darwin" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.Workdir != "" {
		// Bind-mount opt-in: the host path lands at /workspace
		// inside (matches Dockerfile.agent's WORKDIR).
		args = append(args, "-v", opts.Workdir+":/workspace")
		args = append(args, "--label", LabelKey+"-workdir="+opts.Workdir)
	}

	// Env composition (last write wins on collision):
	//
	//   1. harness EnvFromHost  — harness-specific auth (e.g.
	//                              ANTHROPIC_API_KEY for claude-code)
	//   2. social-skills passthrough — operator's .env / shell
	//                                   provider keys (BRAVE, TAVILY,
	//                                   ...) so social-fetch's chains
	//                                   work inside the container
	//   3. opts.Env             — explicit `--env KEY=VAL` overrides
	//   4. CredentialsBlob      — extracted OAuth credentials, if any
	hostEnv := parseEnviron(os.Environ())
	envForContainer, err := h.EnvFromHost(hostEnv)
	if err != nil {
		return nil, fmt.Errorf("harness %s: env: %w", hName, err)
	}
	for k, v := range agent.BuildPassthroughEnv(hostEnv) {
		if _, set := envForContainer[k]; !set {
			envForContainer[k] = v
		}
	}
	for k, v := range opts.Env {
		envForContainer[k] = v
	}
	if opts.CredentialsBlob != "" {
		envForContainer["CLAUDE_OAUTH_CREDENTIALS"] = opts.CredentialsBlob
	}
	for k, v := range envForContainer {
		// Rewrite loopback URLs to host.docker.internal so values
		// like SOCIAL_FETCH_HEADLESS_DAEMON_URL=http://127.0.0.1:5560
		// — fine on the host, wrong inside a container — Just
		// Work without the operator having to remember the magic
		// hostname. We pair this with the --add-host above so the
		// rewrite resolves on Linux + macOS alike. Only URL-shaped
		// values get rewritten; non-URL values pass through unchanged.
		args = append(args, "-e", k+"="+rewriteLoopbackURL(v))
	}

	// Optional caller-supplied labels.
	for k, v := range opts.Labels {
		args = append(args, "--label", k+"="+v)
	}

	args = append(args, image)
	// Default CMD is "sleep" (entrypoint keeps the container alive
	// via tail -f /dev/null). Run() overrides this for the one-shot
	// path.

	cid, err := dockerOutput(ctx, args)
	if err != nil {
		return nil, err
	}
	cid = strings.TrimSpace(cid)

	// Resolve the host-side port for the artifacts server. Best-
	// effort: a missing or unparseable mapping leaves ArtifactsURL
	// empty, and `social-agent pull` surfaces a clear error rather
	// than a confusing dial-tcp failure.
	artifactsURL, _ := p.resolveArtifactsURL(ctx, cid)
	// Wait for the artifacts server to be reachable before
	// returning the session. The server starts in the background
	// from the entrypoint; without this wait, a fast follow-up
	// `pull` races the bind() and gets connection-refused.
	if artifactsURL != "" {
		_ = waitArtifactsReady(ctx, artifactsURL, 5*time.Second)
	}

	return &agent.Session{
		ID:       cid,
		Provider: p.Name(),
		Harness:  hName,
		Image:    image,
		Workdir:  opts.Workdir,
		Created:  time.Now(),
		State:    "running",
		Labels: map[string]string{
			LabelKey:              "true",
			LabelKey + "-harness": hName,
			LabelKey + "-image":   image,
			LabelKey + "-workdir": opts.Workdir,
		},
		ArtifactsURL: artifactsURL,
	}, nil
}

// resolveArtifactsURL asks docker which host port maps to the
// container's ArtifactsContainerPort, returning a URL the
// operator's machine can reach. `docker port` output looks like:
//
//	5563/tcp -> 127.0.0.1:54321
//	5563/tcp -> [::1]:54321
//
// We only care about the IPv4 line (matches what -p 127.0.0.1:0:N
// publishes). Returns an error when no mapping is found — the
// caller logs but doesn't fail the Up because the session itself
// is fine, only the artifacts pull is broken.
func (p *Provider) resolveArtifactsURL(ctx context.Context, cid string) (string, error) {
	out, err := dockerOutput(ctx, []string{"port", cid, fmt.Sprintf("%d", ArtifactsContainerPort)})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		// Format: "5563/tcp -> 127.0.0.1:54321". Some docker
		// versions also emit the bare host:port without the
		// "5563/tcp -> " prefix; handle both.
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		hostPort := line
		if i := strings.LastIndex(line, "-> "); i >= 0 {
			hostPort = strings.TrimSpace(line[i+3:])
		}
		// Skip IPv6 lines (`[::1]:...`); we publish on IPv4.
		if strings.HasPrefix(hostPort, "[") {
			continue
		}
		return "http://" + hostPort, nil
	}
	return "", fmt.Errorf("no IPv4 port mapping for %d", ArtifactsContainerPort)
}

// Down removes containers by ID. Empty ids = remove every container
// labelled as ours. `docker rm -f` so a still-running container
// gets a SIGKILL — the alternative (graceful stop, then remove)
// pays a 10s docker-stop timeout per container which adds up at
// fleet teardown time.
func (p *Provider) Down(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		owned, err := p.listIDs(ctx)
		if err != nil {
			return err
		}
		ids = owned
	}
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"rm", "-f"}, ids...)
	c := exec.CommandContext(ctx, "docker", args...)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
}

// List returns every container labelled as ours. Reads docker's
// own filter mechanism so we don't have to parse `docker ps`'s
// human-readable output — `--format json` returns one JSON object
// per line.
func (p *Provider) List(ctx context.Context) ([]agent.Session, error) {
	out, err := dockerOutput(ctx, []string{
		"ps", "-a",
		"--no-trunc",
		"--filter", "label=" + LabelKey + "=true",
		"--format", "{{json .}}",
	})
	if err != nil {
		return nil, err
	}
	var sessions []agent.Session
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		// docker ps --format json emits PascalCase keys; CamelCase
		// the few we care about.
		var entry struct {
			ID     string `json:"ID"`
			Names  string `json:"Names"`
			Image  string `json:"Image"`
			State  string `json:"State"`
			Labels string `json:"Labels"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Don't fail the whole list on one parse error —
			// surface to stderr and continue. Docker's format is
			// stable but a future docker-version drift shouldn't
			// brick `social-agent ls` entirely.
			fmt.Fprintf(os.Stderr, "social-agent: skipping malformed docker ps row: %v\n", err)
			continue
		}
		labels := parseDockerLabels(entry.Labels)
		// Re-resolve the host port for each running container —
		// docker assigns these dynamically on -p :0: and the
		// mapping disappears when the container stops, so we
		// can't rely on a label or a cached value at Up time.
		// `pull <stopped-id>` will surface an empty URL, which
		// the CLI translates to a clear error.
		var artifactsURL string
		if entry.State == "running" {
			artifactsURL, _ = p.resolveArtifactsURL(ctx, entry.ID)
		}
		sessions = append(sessions, agent.Session{
			ID:           entry.ID,
			Provider:     p.Name(),
			Harness:      labels[LabelKey+"-harness"],
			Image:        entry.Image,
			Workdir:      labels[LabelKey+"-workdir"],
			State:        entry.State,
			Labels:       labels,
			ArtifactsURL: artifactsURL,
		})
	}
	return sessions, nil
}

// Exec runs a command inside an existing container. Empty cmd = the
// harness's interactive form. Streams stdin/stdout/stderr through
// the supplied opts. Allocates a TTY when opts.TTY is set or when
// stdin is a terminal.
func (p *Provider) Exec(ctx context.Context, id string, opts agent.ExecOpts) error {
	if id == "" {
		return errors.New("docker provider: Exec requires a container id")
	}

	cmd := opts.Cmd
	if len(cmd) == 0 {
		// Default to the harness's interactive form. We need to
		// know which harness this container runs — fetch via
		// inspect rather than asking the caller a second time.
		s, err := p.inspect(ctx, id)
		if err != nil {
			return err
		}
		hName := "claude-code"
		if s != nil && s.Harness != "" {
			hName = s.Harness
		}
		h, err := harness.Get(hName)
		if err != nil {
			return err
		}
		cmd = h.InteractiveCmd()
	}

	args := []string{"exec"}
	if opts.TTY {
		args = append(args, "-it")
	} else {
		args = append(args, "-i")
	}
	// Always go through the entrypoint so credential decoding /
	// env passthrough has a consistent shape. The entrypoint's
	// "exec" mode just exec(2)s the rest of argv.
	args = append(args, id, "/usr/local/bin/docker-agent-entrypoint.sh", "exec")
	args = append(args, cmd...)

	c := exec.CommandContext(ctx, "docker", args...)
	c.Stdin = opts.Stdin
	c.Stdout = opts.Stdout
	c.Stderr = opts.Stderr
	if c.Stdin == nil {
		c.Stdin = os.Stdin
	}
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	return c.Run()
}

// Run is the one-shot path. Two modes:
//
//	default — Up + Exec(InvokePrompt) + (if OutputDir) PullAll + Down.
//	          Claude's stdout streams to os.Stdout in real time;
//	          artifacts are pulled post-run.
//	stream  — Up + emit lifecycle/text/artifact events as JSONL on
//	          stdout while the prompt runs; final Down. No
//	          post-run PullAll — operators consume artifacts
//	          inline as the {kind:"artifact"} events arrive.
//
// Both modes use HTTP for artifact access (even when the session
// has a host-bind-mounted workdir on local docker) — keeps the
// substrate-agnostic code path exercised every run.
func (p *Provider) Run(ctx context.Context, opts agent.UpOpts, prompt string) error {
	hName := opts.Harness
	if hName == "" {
		hName = "claude-code"
	}
	h, err := harness.Get(hName)
	if err != nil {
		return err
	}
	s, err := p.Up(ctx, opts)
	if err != nil {
		return err
	}
	defer func() {
		// Best-effort teardown — bracket Up with Down so a panic
		// or context cancellation doesn't leak the container.
		downCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Down(downCtx, s.ID)
	}()
	if opts.Stream {
		// Prefer stream-json when the harness supports it
		// (claude-code does today). Falls back to the
		// line-buffered text path for harnesses that don't
		// (echo) or for harnesses we haven't migrated yet.
		var streamErr error
		if sj, ok := h.(harness.StreamingJSONHarness); ok {
			streamErr = p.runStreamJSON(ctx, s, sj, prompt, opts.StreamHandler)
		} else {
			streamErr = p.runStream(ctx, s, h, prompt, opts.StreamHandler)
		}
		// Pull artifacts BEFORE the deferred Down fires. Streaming
		// only emits artifact metadata events during the run; this
		// is where bytes actually land on the host. Without it, a
		// one-shot streaming run loses artifacts at teardown
		// (the in-container artifacts URL dies with Down).
		if opts.OutputDir != "" && s.ArtifactsURL != "" {
			c := &artifacts.Client{BaseURL: s.ArtifactsURL}
			if _, _, perr := c.PullAll(ctx, opts.OutputDir); perr != nil {
				fmt.Fprintf(os.Stderr, "social-agent: artifacts pull failed: %v\n", perr)
			}
		}
		return streamErr
	}
	if err := p.Exec(ctx, s.ID, agent.ExecOpts{
		Cmd: h.InvokePrompt(prompt),
		// Run is non-interactive — no TTY. Stdout/stderr stream
		// through to the caller (the social-agent CLI passes
		// os.Stdout / os.Stderr).
	}); err != nil {
		return err
	}
	if opts.OutputDir == "" {
		return nil
	}
	if s.ArtifactsURL == "" {
		fmt.Fprintf(os.Stderr, "social-agent: session %s has no ArtifactsURL — skipping pull (was port 5563 published?)\n", s.ID[:12])
		return nil
	}
	c := &artifacts.Client{BaseURL: s.ArtifactsURL}
	count, bytes, err := c.PullAll(ctx, opts.OutputDir)
	if err != nil {
		// Surface the error but don't propagate — the prompt's
		// already-printed answer is the operator's primary
		// return; an artifact-pull miss is a secondary failure
		// they can retry with `social-agent ...` separately if
		// the session were persistent (it isn't here, so they
		// lose the artifacts; we log so they know).
		fmt.Fprintf(os.Stderr, "social-agent: artifacts pull failed: %v\n", err)
		return nil
	}
	if count == 0 {
		fmt.Fprintln(os.Stderr, "(no artifacts produced)")
	} else {
		fmt.Fprintf(os.Stderr, "pulled %d files (%s) → %s\n", count, humanBytes(bytes), opts.OutputDir)
	}
	return nil
}

// runStream drives event emission while the prompt runs. Three
// concurrent activities:
//
//  1. Exec runs in a goroutine, writing claude's stdout to a
//     pipe.
//  2. The pipe is read line-by-line on the main path; each line
//     becomes a {kind:"text"} event.
//  3. An artifact poller runs in another goroutine, GET'ing
//     /artifacts/ once per second and emitting
//     {kind:"artifact"} events for newly-seen files.
//
// Events flow through the supplied handler; nil handler means
// "default to JSONL on os.Stdout" so the CLI path keeps working.
// In-process consumers (MCP progress notifications, daemon SSE
// surfaces) pass a custom handler instead. All emit calls are
// serialised by emitMu so producers never interleave.
//
// On exec completion (pipe closes), the poller is cancelled, a
// final scan picks up files written in the last interval, and
// {kind:"done"} closes the stream. Lifecycle events bracket the
// whole thing.
func (p *Provider) runStream(ctx context.Context, s *agent.Session, h harness.Harness, prompt string, handler func(streaming.Event)) error {
	var emitMu sync.Mutex
	var stdoutWriter *streaming.Writer
	if handler == nil {
		stdoutWriter = streaming.NewWriter(os.Stdout)
	}
	emit := func(e streaming.Event) {
		emitMu.Lock()
		defer emitMu.Unlock()
		if handler != nil {
			handler(e)
			return
		}
		_ = stdoutWriter.Emit(e)
	}

	emit(streaming.Event{Kind: "session", Status: "up", ID: s.ID})

	// Artifact poller. Shares its `seen` map with the final
	// post-exec scan so we never emit the same path@sha256 twice.
	// Runs until pollCancel fires.
	pollCtx, pollCancel := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	seen := map[string]bool{}
	var seenMu sync.Mutex
	var artClient *artifacts.Client
	if s.ArtifactsURL != "" {
		artClient = &artifacts.Client{BaseURL: s.ArtifactsURL}
		go func() {
			defer close(pollDone)
			pollArtifactsShared(pollCtx, artClient, emit, seen, &seenMu)
		}()
	} else {
		close(pollDone)
	}

	// Exec writer pipe — claude's stdout flows through this; we
	// read line-by-line and emit text events.
	pr, pw := io.Pipe()
	execErrCh := make(chan error, 1)
	go func() {
		err := p.Exec(ctx, s.ID, agent.ExecOpts{
			Cmd:    h.InvokePrompt(prompt),
			Stdout: pw,
			Stderr: os.Stderr, // operator sees stderr unfiltered for diagnostic value
		})
		_ = pw.Close()
		execErrCh <- err
	}()

	// Read claude's stdout line-by-line. Each terminal line ('\n'
	// boundary) becomes one text event. Empty lines pass through
	// so claude's intentional formatting (paragraph breaks)
	// survives.
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024) // tolerate long lines
	for scanner.Scan() {
		emit(streaming.Event{Kind: "text", Content: scanner.Text()})
	}
	scanErr := scanner.Err()

	// Wait for exec to finish so we know the prompt is done.
	execErr := <-execErrCh

	// Stop the poller; do a final synchronous artifacts scan to
	// catch anything written in the last second. Reuses the
	// poller's `seen` map so already-emitted entries don't
	// duplicate.
	pollCancel()
	<-pollDone
	if artClient != nil {
		emitArtifactDeltaLocked(ctx, artClient, emit, seen, &seenMu)
	}

	// Lifecycle close + done. Down happens in the caller's
	// defer; we emit "down" here BEFORE that runs so consumers
	// see the event before the container goes away.
	emit(streaming.Event{Kind: "session", Status: "down", ID: s.ID})

	if execErr != nil {
		emit(streaming.Event{Kind: "done", ExitCode: 1, Error: execErr.Error()})
		return nil // already emitted as event; caller doesn't need a Go error too
	}
	if scanErr != nil {
		emit(streaming.Event{Kind: "error", Error: "scan: " + scanErr.Error()})
	}
	emit(streaming.Event{Kind: "done", ExitCode: 0})
	return nil
}

// runStreamJSON is the stream-json variant of runStream for
// harnesses that implement StreamingJSONHarness (claude-code
// today). Differences from runStream:
//
//   - The prompt is written to claude's stdin as one
//     WrapUserMessage JSONL line, then stdin is closed (one-shot
//     for v0.18.0; multi-turn lands when send arrives).
//   - claude's stdout is parsed line-by-line as JSON. Each line
//     is emitted twice: once as kind="claude_event" with the raw
//     body for clients that want the typed claude schema, and
//     (for "assistant" events) once per text content block as
//     kind="text" so consumers that only care about the prose
//     keep working.
//
// Lifecycle (session up/down) and the artifact poller are shared
// with runStream — same emit-mutex pattern, same shared `seen`
// map for de-duped artifact events.
func (p *Provider) runStreamJSON(ctx context.Context, s *agent.Session, h harness.StreamingJSONHarness, prompt string, handler func(streaming.Event)) error {
	var emitMu sync.Mutex
	var stdoutWriter *streaming.Writer
	if handler == nil {
		stdoutWriter = streaming.NewWriter(os.Stdout)
	}
	emit := func(e streaming.Event) {
		emitMu.Lock()
		defer emitMu.Unlock()
		if handler != nil {
			handler(e)
			return
		}
		_ = stdoutWriter.Emit(e)
	}

	emit(streaming.Event{Kind: "session", Status: "up", ID: s.ID})

	// Artifact poller — identical to runStream.
	pollCtx, pollCancel := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	seen := map[string]bool{}
	var seenMu sync.Mutex
	var artClient *artifacts.Client
	if s.ArtifactsURL != "" {
		artClient = &artifacts.Client{BaseURL: s.ArtifactsURL}
		go func() {
			defer close(pollDone)
			pollArtifactsShared(pollCtx, artClient, emit, seen, &seenMu)
		}()
	} else {
		close(pollDone)
	}

	// Stdin: write the prompt as a user-message JSONL, then
	// close. A goroutine owns the writer so Exec can read until
	// EOF without blocking on us; closing the writer is what
	// makes claude finish and exit.
	stdinR, stdinW := io.Pipe()
	go func() {
		defer stdinW.Close()
		_, _ = stdinW.Write(h.WrapUserMessage(prompt))
	}()

	// Stdout: pipe + JSONL parser.
	pr, pw := io.Pipe()
	execErrCh := make(chan error, 1)
	go func() {
		err := p.Exec(ctx, s.ID, agent.ExecOpts{
			Cmd:    h.StreamJSONCmd(),
			Stdin:  stdinR,
			Stdout: pw,
			Stderr: os.Stderr,
		})
		_ = pw.Close()
		execErrCh <- err
	}()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		// Always emit the raw JSONL — copy because the scanner
		// reuses its buffer.
		bodyCopy := make([]byte, len(line))
		copy(bodyCopy, line)
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			// Not valid JSON — claude shouldn't emit this in
			// stream-json mode but be defensive: surface as
			// raw text so the operator sees something rather
			// than silently dropping a line.
			emit(streaming.Event{Kind: "text", Content: scanner.Text()})
			continue
		}
		emit(streaming.Event{Kind: "claude_event", Body: bodyCopy})

		// Extract assistant prose into kind="text" so consumers
		// that only join text events still produce a readable
		// answer. tool_use / tool_result blocks are intentionally
		// NOT extracted — they live in the raw event for clients
		// that care.
		if t, _ := raw["type"].(string); t == "assistant" {
			if msg, _ := raw["message"].(map[string]any); msg != nil {
				if content, _ := msg["content"].([]any); content != nil {
					for _, c := range content {
						cm, _ := c.(map[string]any)
						if cm == nil {
							continue
						}
						if cm["type"] == "text" {
							if txt, _ := cm["text"].(string); txt != "" {
								emit(streaming.Event{Kind: "text", Content: txt})
							}
						}
					}
				}
			}
		}
	}
	scanErr := scanner.Err()

	execErr := <-execErrCh

	pollCancel()
	<-pollDone
	if artClient != nil {
		emitArtifactDeltaLocked(ctx, artClient, emit, seen, &seenMu)
	}

	emit(streaming.Event{Kind: "session", Status: "down", ID: s.ID})

	if execErr != nil {
		emit(streaming.Event{Kind: "done", ExitCode: 1, Error: execErr.Error()})
		return nil
	}
	if scanErr != nil {
		emit(streaming.Event{Kind: "error", Error: "scan: " + scanErr.Error()})
	}
	emit(streaming.Event{Kind: "done", ExitCode: 0})
	return nil
}

// pollArtifactsShared hits /artifacts/ every 1s and emits an
// event for every file not yet in seen. seen + seenMu are shared
// with the post-exec final scan so a path@sha256 never duplicates.
// Tracked by `path@sha256` (not just path) so a rewrite of the
// same path with new content surfaces as a new event. The emit
// callback is the runStream-level serialised sink.
func pollArtifactsShared(ctx context.Context, c *artifacts.Client, emit func(streaming.Event), seen map[string]bool, mu *sync.Mutex) {
	emitArtifactDeltaLocked(ctx, c, emit, seen, mu)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emitArtifactDeltaLocked(ctx, c, emit, seen, mu)
		}
	}
}

// emitArtifactDeltaLocked lists /artifacts and emits an event for
// every entry not yet in seen. Updates seen in place under the
// mutex so the poller and the final-scan caller don't race.
func emitArtifactDeltaLocked(ctx context.Context, c *artifacts.Client, emit func(streaming.Event), seen map[string]bool, mu *sync.Mutex) {
	entries, err := c.List(ctx)
	if err != nil {
		// Don't spam — one error per second would be noisy.
		// Surface as a single error event, no retry annotation.
		emit(streaming.Event{Kind: "error", Error: "artifacts list: " + err.Error()})
		return
	}
	for _, e := range entries {
		key := e.Path + "@" + e.SHA256
		mu.Lock()
		if seen[key] {
			mu.Unlock()
			continue
		}
		seen[key] = true
		mu.Unlock()
		emit(streaming.Event{
			Kind:   "artifact",
			Path:   e.Path,
			Size:   e.Size,
			SHA256: e.SHA256,
			Mime:   e.Mime,
		})
	}
}

// waitArtifactsReady polls the artifacts server's /health
// endpoint until it answers 200 or the deadline fires. The server
// starts in the background from the entrypoint, so a Up that
// returns before the server has bound the port would race a fast
// follow-up Pull. Best-effort: returns nil on success, the last
// error otherwise. Caller logs but doesn't fail Up — the session
// is fine, only artifact-pull is.
func waitArtifactsReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return lastErr
}

// humanBytes formats a byte count for the post-run log line.
// Coarse — KB/MB/GB only — to keep the line short.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/(1024*1024*1024))
	}
}

// ----- helpers -----

// dockerOutput runs `docker <args...>` and returns combined stdout.
// Stderr goes through to the caller's stderr so docker's progress
// output (image pulls, etc) is visible. Used everywhere we need
// to read docker's output (inspect, ps, run -d → container id).
func dockerOutput(ctx context.Context, args []string) (string, error) {
	c := exec.CommandContext(ctx, "docker", args...)
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// parseEnviron converts an os.Environ()-shaped slice into the
// map shape harness.EnvFromHost expects.
func parseEnviron(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}

// parseDockerLabels splits docker's comma-separated label string
// (the form `docker ps --format '{{.Labels}}'` emits) into a map.
// Format: `key1=val1,key2=val2,…`. Values can't contain commas
// (docker enforces this at run time).
func parseDockerLabels(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}

// listIDs returns just the container IDs of our sessions. Used by
// Down for the "remove all of ours" path.
func (p *Provider) listIDs(ctx context.Context) ([]string, error) {
	out, err := dockerOutput(ctx, []string{
		"ps", "-a", "-q",
		"--filter", "label=" + LabelKey + "=true",
	})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ids = append(ids, line)
		}
	}
	return ids, nil
}

// inspect returns one container's session metadata, or nil + nil
// when the container doesn't exist (so Up can fall through to
// `docker run`). Errors only on actual docker failures.
func (p *Provider) inspect(ctx context.Context, idOrName string) (*agent.Session, error) {
	out, err := dockerOutput(ctx, []string{
		"inspect", idOrName,
		"--format", "{{json .}}",
	})
	if err != nil {
		// `docker inspect` exits 1 when the target doesn't exist.
		// Treat as a clean miss; caller decides what to do.
		return nil, nil
	}
	var entry struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal([]byte(out), &entry); err != nil {
		return nil, fmt.Errorf("inspect %s: parse: %w", idOrName, err)
	}
	if entry.Config.Labels[LabelKey] != "true" {
		// Not one of ours — refuse to manage.
		return nil, nil
	}
	return &agent.Session{
		ID:       entry.ID,
		Provider: "docker",
		Harness:  entry.Config.Labels[LabelKey+"-harness"],
		Image:    entry.Config.Image,
		Workdir:  entry.Config.Labels[LabelKey+"-workdir"],
		State:    entry.State.Status,
		Labels:   entry.Config.Labels,
	}, nil
}

// rewriteLoopbackURL swaps host-loopback names (127.0.0.1,
// 0.0.0.0, localhost) inside `http://`-shape URLs with
// `host.docker.internal`. The container's own loopback is its
// own; reaching the host's loopback requires the magic
// host.docker.internal name (paired with --add-host on the docker
// run argv).
//
// Non-URL values pass through unchanged so plain string env vars
// (FOO=bar) aren't mangled by an over-eager regex.
func rewriteLoopbackURL(v string) string {
	// Skip cheaply when we're sure there's nothing to rewrite.
	if !strings.Contains(v, "://") {
		return v
	}
	// Only rewrite the host portion. The same scheme + path stays
	// intact. We do dumb string-replace on the few well-known
	// loopback substrings rather than url.Parse + Reassemble —
	// less surface for parser drift, and the substring forms are
	// unambiguous when bracketed by `://` and `:` / `/`.
	for _, loopback := range []string{
		"://127.0.0.1:",
		"://127.0.0.1/",
		"://localhost:",
		"://localhost/",
		"://0.0.0.0:",
		"://0.0.0.0/",
	} {
		want := strings.Replace(loopback, "127.0.0.1", "host.docker.internal", 1)
		want = strings.Replace(want, "localhost", "host.docker.internal", 1)
		want = strings.Replace(want, "0.0.0.0", "host.docker.internal", 1)
		v = strings.ReplaceAll(v, loopback, want)
	}
	return v
}

// _ keeps io imported — used by the ExecOpts streaming docs above
// even when none of the helper functions reference it directly.
var _ io.Reader = (*os.File)(nil)
