package daytona

// Provider impl — wraps the Daytona REST client (in this same
// package) into the browser.Provider interface so the
// social-browser daemon can manage a fleet of Daytona-backed
// sandboxes the same way it'd manage any other substrate.
//
// The pre-existing bootDaemons + waitForDaemons logic
// (originally in cmd/social-daytona/cmd_up.go) lives here too so
// Up returns ready-to-serve backends — no extra "wait until the
// chromedp pool comes up" step on the caller's side.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/browser"
)

// LabelKey is what the provider stamps on every sandbox it
// creates — used by List to filter "ours" out of the org's
// global sandbox list.
const LabelKey = "social-daytona"

// Provider is the Daytona-backed implementation of
// browser.Provider. Cheap to construct via NewProvider; reuses
// one *Client across all calls for connection pooling.
type Provider struct {
	c *Client
}

// NewProvider builds a Provider from the env-driven Client. Same
// auth chain as the bare daytona.Client: DAYTONA_API_KEY +
// DAYTONA_ORG_ID required, DAYTONA_API_URL optional.
func NewProvider() (*Provider, error) {
	c, err := New()
	if err != nil {
		return nil, err
	}
	return &Provider{c: c}, nil
}

// Name returns "daytona". Matches what `social-browser provider
// daytona <verb>` parses, and what gets stamped on the
// browser.Backend.Provider field.
func (p *Provider) Name() string { return "daytona" }

// Up creates opts.N sandboxes, boots the in-sandbox daemons via
// `daytona exec`, polls until the chromedp pool is listening,
// then fetches the per-sandbox preview URL + token. Returns
// ready-to-serve Backends — the social-browser daemon can
// forward /fetch immediately.
func (p *Provider) Up(ctx context.Context, opts browser.UpOpts) ([]browser.Backend, error) {
	if opts.N < 1 {
		return nil, fmt.Errorf("daytona up: N must be >= 1")
	}
	image := opts.Image
	if image == "" {
		return nil, fmt.Errorf("daytona up: opts.Image is required (snapshot name e.g. 'social-skills-browser:0.14.0')")
	}

	// Auto-generate one shared MCP_AUTH_TOKEN for the batch when
	// the caller didn't pass one — same model the original
	// social-daytona up used. Operators wiring through a vault
	// pass --token to override.
	mcpToken := opts.Token
	if strings.TrimSpace(mcpToken) == "" {
		mcpToken = randomHex(32)
	}

	// Default sizing matches the previous social-daytona
	// behaviour. Caller can override via opts.{CPU,Memory,Disk}.
	cpu := opts.CPU
	if cpu == 0 {
		cpu = 2
	}
	memory := opts.Memory
	if memory == 0 {
		memory = 2
	}
	disk := opts.Disk
	if disk == 0 {
		disk = 3
	}
	autoStop := opts.AutoStopMin // 0 = never, the right default for fleet use

	results := make([]browser.Backend, 0, opts.N)
	for i := 0; i < opts.N; i++ {
		labels := map[string]string{
			LabelKey:                  "true",
			"social-daytona-instance": fmt.Sprintf("%d", i),
		}
		for k, v := range opts.Labels {
			labels[k] = v
		}

		// Env composition: the in-sandbox social-browser entrypoint
		// (docker-entrypoint.sh) sources tailscale-up.sh, which auto-
		// brings the sandbox onto the operator's tailnet when
		// TS_AUTHKEY is set. Forward it here so a single .env entry
		// on the host puts every spawned browser sandbox on the
		// tailnet automatically. MCP_AUTH_TOKEN was already wired;
		// keep it. Inline pickup (rather than agent.BuildPassthroughEnv)
		// because internal/browser must not import internal/agent —
		// agent already imports the browser-daytona client and a
		// reverse dep would cycle.
		sandboxEnv := map[string]string{"MCP_AUTH_TOKEN": mcpToken}
		for _, k := range []string{"TS_AUTHKEY", "HOST_TAILSCALE_NAME"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				sandboxEnv[k] = v
			}
		}
		req := CreateWorkspaceRequest{
			Image:            image,
			CPU:              cpu,
			Memory:           memory,
			Disk:             disk,
			Target:           opts.Region,
			Env:              sandboxEnv,
			Labels:           labels,
			AutoStopInterval: intPtr(autoStop),
		}
		ws, err := p.c.CreateWorkspace(ctx, req)
		if err != nil {
			// Best-effort: surface the failure but keep going so
			// other instances in the batch still get created.
			results = append(results, browser.Backend{
				ID:       "",
				Provider: p.Name(),
				State:    "failed",
				Labels:   map[string]string{"error": err.Error(), "instance": fmt.Sprintf("%d", i)},
			})
			continue
		}

		// Boot the in-sandbox daemons (chromedp + ledger + MCP)
		// via `daytona exec`. Same setsid+nohup pattern that
		// social-daytona used; Daytona's runner ignores the
		// image's CMD so we have to launch them explicitly.
		_ = bootDaemons(ws.ID)

		// Wait for the chromedp daemon to bind 5556 before we
		// hand the URL out — first-display 502 is a common
		// confusion otherwise.
		waitForDaemons(ws.ID, 30*time.Second)

		preview, err := p.c.GetPreviewURL(ctx, ws.ID, 5556, 0)
		if err != nil {
			results = append(results, browser.Backend{
				ID:       ws.ID,
				Provider: p.Name(),
				State:    "failed",
				Labels:   map[string]string{"error": fmt.Sprintf("preview-url 5556: %v", err), "instance": fmt.Sprintf("%d", i)},
			})
			continue
		}

		results = append(results, browser.Backend{
			ID:       ws.ID,
			Provider: p.Name(),
			URL:      preview.URL,
			Token:    preview.Token,
			State:    "ready",
			Created:  time.Now(),
			Labels:   labels,
		})
	}
	return results, nil
}

// Down deletes sandboxes by id, or — when ids is empty — every
// sandbox carrying our LabelKey.
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

// List returns the current fleet — sandboxes carrying the
// LabelKey. We re-resolve preview URL + token for each so the
// returned Backends are immediately usable; that's one extra API
// call per sandbox vs reading cached state, but the API limit is
// generous and this keeps state simple.
func (p *Provider) List(ctx context.Context) ([]browser.Backend, error) {
	ws, err := p.c.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]browser.Backend, 0, len(ws))
	for _, w := range ws {
		if w.Labels[LabelKey] != "true" {
			continue
		}
		state := "ready"
		if w.State != "started" {
			// Anything not "started" — stopped, archived,
			// failed — surfaces as not-ready so the daemon
			// doesn't pick it.
			state = w.State
		}
		preview, perr := p.c.GetPreviewURL(ctx, w.ID, 5556, 0)
		var url, token string
		if perr == nil {
			url = preview.URL
			token = preview.Token
		} else {
			state = "preview-failed"
		}
		out = append(out, browser.Backend{
			ID:       w.ID,
			Provider: p.Name(),
			URL:      url,
			Token:    token,
			State:    state,
			Labels:   w.Labels,
		})
	}
	return out, nil
}

// RefreshBackend re-resolves a fresh signed preview URL for a
// backend. Daytona's signed URLs rotate per call (each request
// returns a different short-id hostname with a fresh embedded
// auth token, default TTL 3600s) — so refresh has to swap the
// whole URL, not just a header token. Token in the returned
// Backend is intentionally "" because auth lives in the URL.
func (p *Provider) RefreshBackend(ctx context.Context, id string) (browser.Backend, error) {
	preview, err := p.c.GetPreviewURL(ctx, id, 5556, 0)
	if err != nil {
		return browser.Backend{}, err
	}
	return browser.Backend{
		ID:       id,
		Provider: "daytona",
		URL:      preview.URL,
		Token:    "",
		State:    "ready",
	}, nil
}

// ----- helpers (lifted from cmd/social-daytona/cmd_up.go) -----

// bootDaemons launches docker-entrypoint.sh inside the sandbox
// as a detached background tree via `daytona exec`.
//
// Daytona doesn't run the image's ENTRYPOINT / CMD — it substitutes
// its own PID 1 — so post-create exec is the ONLY way to start
// the in-sandbox daemons (chromedp pool + ledger + MCP).
//
// Quoting trap: `daytona exec ID -- bash -c <script>` joins everything
// after `--` with whitespace and re-evaluates inside the sandbox
// shell, so a multi-token script like `setsid nohup ... > log &`
// silently degrades to `setsid` with no command. We work around it
// by wrapping the script in literal single quotes; the outer sandbox
// shell preserves the quoted region as one arg to bash -c.
//
// nohup + redirect-stdin-from-/dev/null + `& disown` makes the
// process tree survive the exec session ending. setsid is dropped
// — it was finicky in some sandbox util-linux builds, and the
// nohup + disown combo is enough.
func bootDaemons(sandboxID string) error {
	// Single-line, single-quoted. Don't put single quotes INSIDE
	// the script — there's no way to escape them while staying
	// inside the outer pair without breaking the daytona-CLI
	// whitespace-join.
	script := `nohup /usr/local/bin/docker-entrypoint.sh all > /tmp/social-skills.log 2>&1 < /dev/null & disown`
	cmd := exec.Command("daytona", "exec", sandboxID, "--", "bash", "-c", "'"+script+"'")
	cmd.Env = ensureDaytonaAPIEnv(os.Environ())
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// waitForDaemons polls the in-sandbox /health (via daytona exec
// curl) until either it responds 200 or timeout fires. Returns
// true on success.
func waitForDaemons(sandboxID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("daytona", "exec", sandboxID, "--",
			"/bin/sh", "-c",
			"curl -fsS -o /dev/null -m 2 http://127.0.0.1:5556/status")
		cmd.Env = ensureDaytonaAPIEnv(os.Environ())
		if err := cmd.Run(); err == nil {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// ensureDaytonaAPIEnv adds DAYTONA_API_URL when missing — the
// daytona CLI's API-key auth path silently rejects credentials
// when DAYTONA_API_URL is empty (it doesn't fall through to its
// own embedded default).
func ensureDaytonaAPIEnv(env []string) []string {
	for _, v := range env {
		if strings.HasPrefix(v, "DAYTONA_API_URL=") && len(v) > len("DAYTONA_API_URL=") {
			return env
		}
	}
	return append(env, "DAYTONA_API_URL=https://app.daytona.io/api")
}

// intPtr returns &n. The CreateWorkspaceRequest's
// AutoStopInterval is *int so we can distinguish "send 0 = never
// auto-stop" from "field absent → use API default".
func intPtr(n int) *int { return &n }

// randomHex is the same shared-token generator that
// social-daytona used pre-rename. Keeps backwards-compatible
// MCP_AUTH_TOKEN format.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
