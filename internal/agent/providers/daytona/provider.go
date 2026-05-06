// Package daytona is the Daytona-substrate Provider for
// social-agent sessions. Mirrors the local-docker provider shape
// (internal/agent/providers/docker) but runs each session in a
// Daytona sandbox instead of a local container. /artifacts pull
// uses the same HTTP path — the only difference is how
// Session.ArtifactsURL is resolved (Daytona preview URL + token
// vs docker port-publish).
//
// Reuses the Daytona REST client from
// internal/browser/providers/daytona — same auth, same workspace
// API. The browser provider already proved out the bootDaemons
// quoting workaround for `daytona exec` so we lift that pattern
// verbatim.
//
// Versioning: lands at v0.17.0 — minor bump because cloud-running
// agent sessions are a new substrate, additive.
package daytona

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/agent"
	"github.com/jedi4ever/social-skills/internal/agent/artifacts"
	"github.com/jedi4ever/social-skills/internal/agent/harness"
	browserdaytona "github.com/jedi4ever/social-skills/internal/browser/providers/daytona"
)

// LabelKey is what the provider stamps on every sandbox it
// creates. List filters by this label so List() returns only
// agent-flavoured workspaces, not browser-pool ones (the browser
// provider stamps "social-daytona=true" — a different label, so
// the two coexist on one Daytona org).
const LabelKey = "social-agent"

// ArtifactsContainerPort is what the in-container `social-agent
// artifacts serve` listens on. Same as the docker provider —
// Daytona's preview URL surfaces this same port externally as
// `https://5563-<sandbox-id>.daytonaproxy01.net`.
const ArtifactsContainerPort = 5563

// Provider is the Daytona-substrate impl. Wraps the bare daytona
// REST client (reused from internal/browser/providers/daytona) so
// each call goes straight to Daytona's API without a per-method
// re-init.
type Provider struct {
	c *browserdaytona.Client
}

// NewProvider constructs a Provider from the env-driven REST
// client. Same auth chain as the browser daytona provider:
// DAYTONA_API_KEY + DAYTONA_ORG_ID required, DAYTONA_API_URL
// optional.
func NewProvider() (*Provider, error) {
	c, err := browserdaytona.New()
	if err != nil {
		return nil, err
	}
	return &Provider{c: c}, nil
}

// Name identifies the provider for Session.Provider stamping and
// CLI output.
func (p *Provider) Name() string { return "daytona" }

// Up creates a sandbox running the social-skills-agent image,
// boots the in-sandbox daemons (artifacts server + ledger + MCP)
// via `daytona exec`, polls until the artifacts server's /health
// answers, then resolves the preview URL + token. Returns a
// ready-to-use Session.
func (p *Provider) Up(ctx context.Context, opts agent.UpOpts) (*agent.Session, error) {
	hName := opts.Harness
	if hName == "" {
		hName = "claude-code"
	}
	if _, err := harness.Get(hName); err != nil {
		return nil, err
	}
	image := opts.Image
	if image == "" {
		image = "social-skills-agent:latest"
	}
	// Workdir bind-mount is meaningless on Daytona (no host).
	// The operator pre-populates /artifacts via push (future) or
	// just leans on social-fetch / the bundled binaries to fetch
	// inputs from the network. Surface a clear note rather than
	// silently dropping the flag.
	if opts.Workdir != "" {
		fmt.Fprintf(os.Stderr, "social-agent: --workdir %q has no effect on the daytona provider (no host bind-mount); ignoring\n", opts.Workdir)
	}

	// Compose env from harness + caller.
	envForContainer, err := buildEnv(hName, opts)
	if err != nil {
		return nil, err
	}

	// Create the sandbox.
	labels := map[string]string{
		LabelKey:              "true",
		LabelKey + "-harness": hName,
		LabelKey + "-image":   image,
	}
	for k, v := range opts.Labels {
		labels[k] = v
	}
	cpu, memory, disk := defaultResources()
	req := browserdaytona.CreateWorkspaceRequest{
		Image:            image,
		CPU:              cpu,
		Memory:           memory,
		Disk:             disk,
		Env:              envForContainer,
		Labels:           labels,
		AutoStopInterval: intPtr(0), // never auto-stop; operator drives lifecycle via Down
	}
	ws, err := p.c.CreateWorkspace(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("daytona create: %w", err)
	}

	// Boot the in-sandbox daemons. Daytona doesn't run the
	// image's ENTRYPOINT/CMD — it substitutes its own PID 1 —
	// so post-create exec is the only way to start anything.
	// Reuse the browser provider's documented quoting workaround.
	if err := bootAgent(ws.ID); err != nil {
		fmt.Fprintf(os.Stderr, "social-agent: daytona boot failed: %v (sandbox stays up; debug via `daytona exec %s -- ...`)\n", err, ws.ID)
	}

	// Resolve artifacts preview URL + token. Wait for the
	// in-sandbox artifacts server to answer /health before
	// returning so a fast follow-up Pull doesn't race the
	// background daemon startup.
	artifactsURL, token, err := p.resolveArtifactsURL(ctx, ws.ID)
	if err != nil {
		// Sandbox is up; we just couldn't get a preview URL.
		// Surface but don't fail the session.
		fmt.Fprintf(os.Stderr, "social-agent: preview-url failed: %v\n", err)
	}
	if artifactsURL != "" {
		client := &artifacts.Client{BaseURL: artifactsURL, Token: token}
		_ = waitForArtifactsServer(ctx, client, 60*time.Second)
	}

	return &agent.Session{
		ID:           ws.ID,
		Provider:     p.Name(),
		Harness:      hName,
		Image:        image,
		Workdir:      "", // not applicable on Daytona
		Created:      time.Now(),
		State:        "running",
		Labels:       labels,
		ArtifactsURL: artifactsURL,
		// Token isn't on agent.Session today — the
		// artifacts.Client holds it. Stash it on Labels for
		// debugging / for List to re-attach.
	}, nil
}

// Down deletes one or more sandboxes by ID. Empty ids = remove
// every sandbox carrying our LabelKey.
func (p *Provider) Down(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		ws, err := p.c.ListWorkspaces(ctx)
		if err != nil {
			return err
		}
		for _, w := range ws {
			if w.Labels[LabelKey] == "true" {
				ids = append(ids, w.ID)
			}
		}
	}
	var firstErr error
	for _, id := range ids {
		if err := p.c.DeleteWorkspace(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// List enumerates sandboxes with our LabelKey. Re-resolves the
// artifacts preview URL + token for each so the returned Session
// is immediately usable — that's one extra API call per sandbox
// vs cached state, but Daytona's API limit is generous and the
// alternative (cached tokens that go stale after 1h) is worse UX.
func (p *Provider) List(ctx context.Context) ([]agent.Session, error) {
	ws, err := p.c.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]agent.Session, 0, len(ws))
	for _, w := range ws {
		if w.Labels[LabelKey] != "true" {
			continue
		}
		state := "running"
		if w.State != "started" {
			state = w.State
		}
		var artifactsURL, token string
		if state == "running" {
			if u, t, err := p.resolveArtifactsURL(ctx, w.ID); err == nil {
				artifactsURL, token = u, t
			}
		}
		_ = token // see Up's comment — agent.Session has no Token field today
		out = append(out, agent.Session{
			ID:           w.ID,
			Provider:     p.Name(),
			Harness:      w.Labels[LabelKey+"-harness"],
			Image:        w.Labels[LabelKey+"-image"],
			State:        state,
			Labels:       w.Labels,
			ArtifactsURL: artifactsURL,
		})
	}
	return out, nil
}

// Exec runs a command inside a sandbox. Streams stdin/stdout/
// stderr through `daytona exec`. PTY mode is best-effort —
// `daytona exec`'s -t flag isn't always interactive; for serious
// PTY work the operator can `daytona ssh` directly.
func (p *Provider) Exec(ctx context.Context, id string, opts agent.ExecOpts) error {
	if id == "" {
		return fmt.Errorf("daytona provider: Exec requires a sandbox id")
	}
	cmd := opts.Cmd
	if len(cmd) == 0 {
		// Default to the harness's interactive form. Look up the
		// harness via List (we stamped it as a label at Up time).
		hName := "claude-code"
		if sessions, err := p.List(ctx); err == nil {
			for _, s := range sessions {
				if s.ID == id && s.Harness != "" {
					hName = s.Harness
					break
				}
			}
		}
		h, err := harness.Get(hName)
		if err != nil {
			return err
		}
		cmd = h.InteractiveCmd()
	}

	args := append([]string{"exec", id, "--"}, cmd...)
	c := exec.CommandContext(ctx, "daytona", args...)
	c.Env = ensureDaytonaAPIEnv(os.Environ())
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

// Run is the one-shot path: Up + Exec(InvokePrompt) + (if
// OutputDir or Stream) handle artifacts + Down. Mirrors the
// docker provider's Run shape; the substrate-specific bits are
// already isolated in Up / Exec / Down.
//
// For now we only implement the non-streaming path; --stream will
// be wired in a follow-up commit because the artifact-poller +
// JSONL transformer in the docker provider isn't substrate-aware
// yet (it should be hoisted out so both providers share it).
// Streaming via daytona returns a clear "not yet" error rather
// than half-working output.
func (p *Provider) Run(ctx context.Context, opts agent.UpOpts, prompt string) error {
	if opts.Stream {
		return fmt.Errorf("daytona provider: --stream is not yet implemented (use the docker provider for streaming, or pull /artifacts post-run)")
	}
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
		downCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Down(downCtx, s.ID)
	}()
	if err := p.Exec(ctx, s.ID, agent.ExecOpts{Cmd: h.InvokePrompt(prompt)}); err != nil {
		return err
	}
	if opts.OutputDir == "" {
		return nil
	}
	if s.ArtifactsURL == "" {
		fmt.Fprintf(os.Stderr, "social-agent: session %s has no ArtifactsURL — skipping pull\n", s.ID[:12])
		return nil
	}
	// Resolve token again so we can attach it to the client. The
	// preview-URL token rotates per call; the one we cached at
	// Up may already be near expiry.
	_, token, err := p.resolveArtifactsURL(ctx, s.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "social-agent: token refresh failed: %v\n", err)
	}
	c := &artifacts.Client{BaseURL: s.ArtifactsURL, Token: token}
	count, bytes, err := c.PullAll(ctx, opts.OutputDir)
	if err != nil {
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

// ----- helpers -----

// resolveArtifactsURL fetches a fresh preview URL + token for the
// sandbox's artifacts port (5563). Returns the URL and the
// per-request preview-token Daytona issues — the artifacts
// client attaches both as Authorization: Bearer + X-Daytona-
// Preview-Token headers (same dual-header shape the chromedp
// pool uses).
func (p *Provider) resolveArtifactsURL(ctx context.Context, sandboxID string) (string, string, error) {
	preview, err := p.c.GetPreviewURL(ctx, sandboxID, ArtifactsContainerPort, 0)
	if err != nil {
		return "", "", err
	}
	return preview.URL, preview.Token, nil
}

// bootAgent is the agent-flavoured equivalent of the browser
// provider's bootDaemons. The Dockerfile.agent's entrypoint is
// `docker-agent-entrypoint.sh`; we exec it with the default
// "sleep" arg so the entrypoint does the artifact-server
// background-launch + then keeps PID alive. Same single-quoted
// wrapper trick the browser provider uses to survive the daytona
// CLI's whitespace-joining.
func bootAgent(sandboxID string) error {
	script := `nohup /usr/local/bin/docker-agent-entrypoint.sh sleep > /tmp/social-skills.log 2>&1 < /dev/null & disown`
	cmd := exec.Command("daytona", "exec", sandboxID, "--", "bash", "-c", "'"+script+"'")
	cmd.Env = ensureDaytonaAPIEnv(os.Environ())
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// waitForArtifactsServer pings the artifacts /health endpoint
// until it answers 200 or the timeout fires. Best-effort: if
// the server isn't ready by deadline we still return the session
// and the operator's Pull will surface its own clear error.
func waitForArtifactsServer(ctx context.Context, c *artifacts.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	httpc := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
		if err != nil {
			return err
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
			req.Header.Set("X-Daytona-Preview-Token", c.Token)
		}
		resp, err := httpc.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("artifacts server didn't answer within %s", timeout)
}

// buildEnv composes the env map injected into the sandbox.
// Same order as the docker provider:
//
//  1. harness EnvFromHost  — claude-code's auth keys
//  2. social-skills passthrough — operator's provider keys
//     (BRAVE, TAVILY, …) so the
//     in-sandbox social-fetch's
//     chains work
//  3. opts.Env             — explicit `--env` overrides
//  4. CredentialsBlob
//
// Loopback URL rewriting is NOT needed on Daytona — sandboxes
// have direct internet egress, so SOCIAL_FETCH_HEADLESS_DAEMON_URL
// pointing at a public daytona-proxy URL just works.
func buildEnv(hName string, opts agent.UpOpts) (map[string]string, error) {
	h, err := harness.Get(hName)
	if err != nil {
		return nil, err
	}
	hostEnv := parseEnviron(os.Environ())
	env, err := h.EnvFromHost(hostEnv)
	if err != nil {
		return nil, fmt.Errorf("harness %s: env: %w", hName, err)
	}
	for k, v := range agent.BuildPassthroughEnv(hostEnv) {
		if _, set := env[k]; !set {
			env[k] = v
		}
	}
	for k, v := range opts.Env {
		env[k] = v
	}
	if opts.CredentialsBlob != "" {
		env["CLAUDE_OAUTH_CREDENTIALS"] = opts.CredentialsBlob
	}
	return env, nil
}

// parseEnviron mirrors the docker provider's helper. Kept private
// per-package to avoid an internal/agent/util churn for one func.
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

// defaultResources returns the per-sandbox CPU / memory / disk
// for a fresh agent session. Same defaults the browser pool
// uses; right-size later if claude-code session memory pressure
// shows up.
func defaultResources() (cpu, memory, disk int) {
	return 2, 2, 3
}

// ensureDaytonaAPIEnv adds DAYTONA_API_URL when missing — the
// daytona CLI's API-key auth path silently rejects credentials
// when DAYTONA_API_URL is empty. Lifted from the browser
// provider for the same reason.
func ensureDaytonaAPIEnv(env []string) []string {
	for _, v := range env {
		if strings.HasPrefix(v, "DAYTONA_API_URL=") && len(v) > len("DAYTONA_API_URL=") {
			return env
		}
	}
	return append(env, "DAYTONA_API_URL=https://app.daytona.io/api")
}

// intPtr returns &n. CreateWorkspaceRequest.AutoStopInterval is
// *int so we can distinguish "send 0 = never auto-stop" from
// "field absent".
func intPtr(n int) *int { return &n }

// humanBytes formats a byte count for the post-run log line.
// Lifted from the docker provider — small enough to dup rather
// than create internal/agent/util just for this.
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

// _ keeps io imported for future streaming-mode wiring (we'll
// pipe Exec stdout through it).
var _ io.Reader = (*os.File)(nil)
