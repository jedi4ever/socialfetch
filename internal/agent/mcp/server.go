// Package mcp exposes social-agent's CLI subcommands as MCP
// tools. Mirrors internal/mcp/server.go (social-fetch's MCP) but
// scoped to agent-session lifecycles: run, up, exec, ls, down,
// pull, rm-file, harness-list.
//
// Lets Claude Desktop / claude.ai / Claude Code drive social-agent
// as a tool — useful when an outer claude session wants to
// delegate a subtask to a sandboxed inner claude.
//
// stdio is the v1 transport (one process, one client, no auth).
// HTTP / ngrok / bearer-token, mirroring social-fetch's MCP, is a
// follow-up if we need it.
package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/agent"
	"github.com/jedi4ever/social-skills/internal/agent/artifacts"
	"github.com/jedi4ever/social-skills/internal/agent/elicitcb"
	"github.com/jedi4ever/social-skills/internal/agent/harness"
	dockerprov "github.com/jedi4ever/social-skills/internal/agent/providers/docker"
	"github.com/jedi4ever/social-skills/internal/agent/streaming"
)

// progressSummary turns a streaming.Event into a short human-
// readable status string for the MCP progress notification's
// `message` field. Per MCP spec, that field is for short scannable
// updates the client renders inline (status line, transcript,
// etc); the full typed event would belong somewhere else. Returns
// "" for events the client doesn't need to see (claude_event is
// noise — it duplicates the text events we emit alongside).
func progressSummary(e streaming.Event) string {
	switch e.Kind {
	case "text":
		s := strings.TrimSpace(e.Content)
		if s == "" {
			return ""
		}
		// Keep these tight — clients render in cramped UI.
		if len(s) > 160 {
			s = s[:157] + "..."
		}
		return s
	case "artifact":
		return fmt.Sprintf("wrote %s (%d bytes, %s)", e.Path, e.Size, e.Mime)
	case "session":
		id := e.ID
		if len(id) > 12 {
			id = id[:12]
		}
		if e.Status == "up" {
			return "session up: " + id
		}
		return "session down: " + id
	case "done":
		if e.Error != "" {
			return "done with error: " + e.Error
		}
		return fmt.Sprintf("done (exit %d)", e.ExitCode)
	case "error":
		return "error: " + e.Error
	}
	// claude_event (stream-json raw) and any future kinds we don't
	// yet have UI strings for — skip the notification rather than
	// confuse the client with empty or technical messages.
	return ""
}

// newSessionDir creates a per-run session root under
// $TMPDIR/social-agent/<id>/ with `artifacts/` and `inputs/`
// subdirs. The session root parallels the in-container
// session-scoped state: artifacts/ mirrors /artifacts/ (outbound),
// inputs/ mirrors /inputs/ (inbound, read-only in the container),
// leaving room for future per-run material (logs, transcripts,
// an isolated .claude/ homedir).
//
// Without this, streaming one-shot runs lose their artifacts at
// teardown — the in-container artifacts HTTP server dies with the
// container, and a buffered post-run pull has nowhere to reach.
//
// Name format: <RFC3339-compact>-<8-hex>. Time-prefix sorts
// naturally in `ls`; hex suffix prevents collisions when two runs
// land in the same second. Directories are created with 0o755 so
// the operator can read pulled artifacts directly. Cleanup TTL is
// a follow-up.
func newSessionDir() (root, artifactsDir, inputsDir string, err error) {
	var rand8 [4]byte
	if _, e := rand.Read(rand8[:]); e != nil {
		return "", "", "", fmt.Errorf("rand: %w", e)
	}
	name := time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(rand8[:])
	root = filepath.Join(os.TempDir(), "social-agent", name)
	artifactsDir = filepath.Join(root, "artifacts")
	inputsDir = filepath.Join(root, "inputs")
	for _, d := range []string{artifactsDir, inputsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", "", "", fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return root, artifactsDir, inputsDir, nil
}

// stageInputs copies operator-supplied host files into the
// session's inputs dir so they're visible at /inputs in the
// container via bind-mount. Each path lands at
// inputsDir/<basename> — directories are rejected (operators
// can tar them if they really need a tree). Returns the list of
// staged destination paths so the caller can echo them back.
func stageInputs(hostPaths []string, inputsDir string) ([]string, error) {
	staged := make([]string, 0, len(hostPaths))
	for _, src := range hostPaths {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		info, err := os.Stat(src)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", src, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("input %s is a directory; only files are supported (tar it first)", src)
		}
		dst := filepath.Join(inputsDir, filepath.Base(src))
		if err := copyFile(src, dst); err != nil {
			return nil, fmt.Errorf("copy %s → %s: %w", src, dst, err)
		}
		staged = append(staged, dst)
	}
	return staged, nil
}

// copyFile copies one file byte-for-byte using io.Copy so large
// inputs (PDFs, screenshots) don't spike RAM.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// Config holds the per-server settings. Mirrors social-fetch
// MCP's Config — kept small for this v1.
type Config struct {
	// Version stamps the MCP server's version string. Bumped in
	// lockstep with the binary's Version constant.
	Version string

	// DefaultImage overrides the docker provider's default
	// image:tag. Empty = "social-skills-agent:<Version>"; allows
	// operators running multiple agent images to pick one for
	// MCP-driven sessions.
	DefaultImage string
}

// NewServer builds an MCP server with all social-agent tools
// registered. Caller runs it via server.ServeStdio for stdio
// transport.
func NewServer(cfg Config) *server.MCPServer {
	hooks := &server.Hooks{}
	// Capture the client's declared capabilities at initialize
	// time so the probe tool can report them. Stored on a
	// package-level pointer (see clientCaps below) — there's
	// only ever one client per stdio server, so a single slot
	// is sufficient.
	hooks.AddAfterInitialize(func(_ context.Context, _ any, req *mcpgo.InitializeRequest, _ *mcpgo.InitializeResult) {
		caps := req.Params.Capabilities
		clientCaps.Store(&caps)
	})

	s := server.NewMCPServer(
		"social-agent",
		cfg.Version,
		server.WithToolCapabilities(false),
		server.WithHooks(hooks),
	)
	registerTools(s, cfg)
	return s
}

func registerTools(s *server.MCPServer, cfg Config) {
	addRunTool(s, cfg)
	addUpTool(s, cfg)
	addExecTool(s, cfg)
	addLsTool(s, cfg)
	addDownTool(s, cfg)
	addPullTool(s, cfg)
	addUploadTool(s, cfg)
	addRmFileTool(s, cfg)
	addHarnessListTool(s, cfg)
	addProbeClientTool(s, cfg)
}

// clientCaps is a single-slot cache of the client's declared
// capabilities, populated by an AfterInitialize hook. Used by the
// probe tool to surface what the connected client supports.
// atomic.Value would also work — sync.Map is just convenient for
// the typed-pointer-or-nil pattern.
var clientCaps capsSlot

type capsSlot struct {
	mu  sync.Mutex
	val *mcpgo.ClientCapabilities
}

func (c *capsSlot) Store(v *mcpgo.ClientCapabilities) {
	c.mu.Lock()
	c.val = v
	c.mu.Unlock()
}

func (c *capsSlot) Load() *mcpgo.ClientCapabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val
}

// buildProvider returns the docker provider — the only substrate
// today. Future daytona support plugs in with a router on a
// `provider` arg.
func buildProvider() agent.Provider {
	return dockerprov.New()
}

// resolveImage applies the DefaultImage override or falls back to
// the version-stamped tag.
func resolveImage(cfg Config) string {
	if cfg.DefaultImage != "" {
		return cfg.DefaultImage
	}
	return "social-skills-agent:" + cfg.Version
}

// ---- run -------------------------------------------------------

type runArgs struct {
	Prompt  string            `json:"prompt"`
	Harness string            `json:"harness,omitempty"`
	Workdir string            `json:"workdir,omitempty"`
	Output  string            `json:"output,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Image   string            `json:"image,omitempty"`
	// Inputs is a list of host paths to stage into the
	// session's /inputs/ directory before exec. Files are copied
	// into <session-dir>/inputs/<basename> and bind-mounted
	// read-only at /inputs in the container. Lets the operator
	// hand the agent files (PDF, notes, screenshots) without
	// exposing the rest of the filesystem.
	Inputs []string `json:"inputs,omitempty"`
	// Stream is a *bool so omitted-from-JSON (nil) is
	// distinguishable from explicit-false. nil = stream when a
	// progressToken is available (the default); *false = always
	// buffer-and-return, useful for tests that want a single
	// envelope back regardless of the client's progressToken.
	Stream *bool `json:"stream,omitempty"`
}

func addRunTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_run",
		mcpgo.WithDescription("Run a one-shot prompt inside a sandboxed claude-code container, return claude's response on stdout. The container is created, prompt executed, container removed — single round trip. Use `output` to also pull files claude wrote to /artifacts/ to a host directory. Use `workdir` to bind-mount the host repo so claude can read existing files; otherwise the container has no host filesystem access. `env` is a map of additional env vars (e.g. SOCIAL_FETCH_HEADLESS_DAEMON_URL) to inject into the container — loopback URLs auto-rewrite to host.docker.internal so the container can reach host services. `harness` defaults to claude-code; pass `echo` for an auth-free smoke test. Streaming is on by default: when the client sends a `_meta.progressToken`, this tool emits one `notifications/progress` event per session-up / text / artifact / session-down / done event; the final tool result still carries the aggregated text + artifact list. Set `stream: false` to force buffer-and-return regardless of the progressToken — useful for tests."),
		mcpgo.WithString("prompt", mcpgo.Required(), mcpgo.Description("The prompt to run. Plain English; the harness's CLI flags are added automatically.")),
		mcpgo.WithString("harness", mcpgo.Description("Coding-agent CLI to run inside (claude-code | echo). Default: claude-code.")),
		mcpgo.WithString("workdir", mcpgo.Description("Host path bind-mounted at /workspace. Default: no mount (sandboxed).")),
		mcpgo.WithString("output", mcpgo.Description("Host directory to pull /artifacts to after the run. Default: skip pull.")),
		mcpgo.WithObject("env", mcpgo.Description("Additional env vars to set inside the container. Loopback URLs are auto-rewritten so the container can reach host services.")),
		mcpgo.WithString("image", mcpgo.Description("Override the docker image:tag. Default: social-skills-agent:<Version>.")),
		mcpgo.WithArray("inputs", mcpgo.Description("List of host file paths to copy into the session's inputs/ dir, bind-mounted read-only at /inputs in the container. Lets the operator hand the agent files to work on without exposing the rest of the filesystem. Files only — directories are rejected. Items: type=string.")),
		mcpgo.WithBoolean("stream", mcpgo.Description("Force streaming on/off. Omit (default) = stream when the client sent a progressToken. false = buffer-and-return regardless. true is equivalent to omitting it.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, req mcpgo.CallToolRequest, args runArgs) (*mcpgo.CallToolResult, error) {
		if strings.TrimSpace(args.Prompt) == "" {
			return mcpgo.NewToolResultError("prompt is required"), nil
		}
		prov := buildProvider()
		image := args.Image
		if image == "" {
			image = resolveImage(cfg)
		}
		hName := args.Harness
		if hName == "" {
			hName = "claude-code"
		}

		// Streaming default: on when the client sent a progress
		// token. Explicit `stream: false` forces the buffered
		// path even when a token exists (test escape hatch).
		// Buffered path also runs when there's no token at all —
		// streaming requires somewhere to send notifications.
		var progressToken any
		if req.Params.Meta != nil {
			progressToken = req.Params.Meta.ProgressToken
		}
		streamRequested := args.Stream == nil || *args.Stream
		if streamRequested && progressToken != nil {
			return runStreaming(ctx, s, prov, progressToken, args, image, hName)
		}

		h, err := harness.Get(hName)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}

		// Always allocate a session root up-front so /inputs/ can
		// be bind-mounted at create time (you can't add a docker
		// bind-mount to a running container). args.Output, when
		// set, overrides where artifacts get pulled but doesn't
		// replace the session root — the session dir still exists
		// for inputs and any future per-session state.
		sessionRoot, artifactsDir, inputsDir, dirErr := newSessionDir()
		if dirErr != nil {
			return mcpgo.NewToolResultError("session dir: " + dirErr.Error()), nil
		}
		if _, err := stageInputs(args.Inputs, inputsDir); err != nil {
			return mcpgo.NewToolResultError("stage inputs: " + err.Error()), nil
		}
		outDir := args.Output
		if outDir == "" {
			outDir = artifactsDir
		}

		sess, err := prov.Up(ctx, agent.UpOpts{
			Image:     image,
			Harness:   hName,
			Workdir:   args.Workdir,
			Env:       args.Env,
			InputsDir: inputsDir,
		})
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		// Best-effort teardown.
		defer func() {
			downCtx := context.Background()
			_ = prov.Down(downCtx, sess.ID)
		}()

		var stdout, stderr bytes.Buffer
		if err := prov.Exec(ctx, sess.ID, agent.ExecOpts{
			Cmd:    h.InvokePrompt(args.Prompt),
			Stdout: &stdout,
			Stderr: &stderr,
		}); err != nil {
			// Include any partial stdout/stderr in the error so
			// the agent has signal even when the run itself
			// failed.
			msg := fmt.Sprintf("exec: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			return mcpgo.NewToolResultError(msg), nil
		}

		var pulledFiles []string
		if outDir != "" && sess.ArtifactsURL != "" {
			c := &artifacts.Client{BaseURL: sess.ArtifactsURL}
			entries, err := c.List(ctx)
			if err == nil {
				for _, e := range entries {
					dst := filepath.Join(outDir, e.Path)
					if err := c.GetTo(ctx, e.Path, dst); err != nil {
						continue
					}
					pulledFiles = append(pulledFiles, dst)
				}
			}
		}

		envelope := map[string]any{
			"text":          stdout.String(),
			"artifacts":     pulledFiles,
			"artifacts_dir": outDir,
			"inputs_dir":    inputsDir,
			"session_dir":   sessionRoot,
		}
		body, _ := json.Marshal(envelope)
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// runStreaming drives Provider.Run with Stream=true and a
// handler that converts each streaming.Event into a
// notifications/progress notification on the supplied
// progressToken. Text events accumulate into the final tool
// result so callers that ignore progress notifications still get
// the full output. Artifact events are recorded by path in the
// response — bodies aren't fetched here (the run-time pull would
// race against in-progress writes); callers grab them with
// social_agent_pull post-run.
//
// progress is monotonic-increasing across events (1, 2, 3, …) so
// clients that bucket-by-progress get strict ordering. message is
// a short human-readable summary; data is the full Event JSON
// for clients that want to drive UI off it.
func runStreaming(ctx context.Context, srv *server.MCPServer, prov agent.Provider, token any, args runArgs, image, hName string) (*mcpgo.CallToolResult, error) {
	var (
		mu          sync.Mutex
		textLines   []string
		artifactLst []map[string]any
		progress    float64
		exitCode    int
		runErr      string
	)

	// Start the elicitation callback server. The inner claude's
	// ask_user tool calls back to this URL when it wants the
	// outer Claude Code's user to answer a question. Loopback-
	// only + bearer-token-gated; lifetime = this run.
	//
	// Important: RequestElicitation looks up the active client
	// session via ClientSessionFromContext, so we must use the
	// outer tool-call ctx (which carries the session) rather than
	// the elicitcb HTTP handler's request ctx (which doesn't).
	cb, err := elicitcb.Start(func(elicitCtx context.Context, question string) (string, bool, error) {
		req := mcpgo.ElicitationRequest{
			Request: mcpgo.Request{Method: string(mcpgo.MethodElicitationCreate)},
			Params: mcpgo.ElicitationParams{
				Message: question,
				RequestedSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"answer": map[string]any{
							"type":        "string",
							"description": "Your reply to the inner agent's question.",
						},
					},
					"required": []string{"answer"},
				},
			},
		}
		result, err := srv.RequestElicitation(ctx, req)
		if err != nil {
			return "", false, err
		}
		if result.Action != "accept" {
			return "", false, nil
		}
		// Content is map[string]any; extract the answer field.
		if cm, ok := result.Content.(map[string]any); ok {
			if v, ok := cm["answer"].(string); ok {
				return v, true, nil
			}
		}
		return "", true, nil // accepted but blank
	})
	if err != nil {
		return mcpgo.NewToolResultError("elicitcb start: " + err.Error()), nil
	}
	defer func() { _ = cb.Close() }()

	// Merge the callback URL + token into the env passed to the
	// container. The docker provider's rewriteLoopbackURL turns
	// 127.0.0.1 into host.docker.internal so the container can
	// reach back; the token is opaque and travels verbatim.
	envWithCb := map[string]string{}
	for k, v := range args.Env {
		envWithCb[k] = v
	}
	envWithCb["SOCIAL_AGENT_CALLBACK_URL"] = cb.URL()
	envWithCb["SOCIAL_AGENT_CALLBACK_TOKEN"] = cb.Token()

	handler := func(e streaming.Event) {
		mu.Lock()
		progress++
		seq := progress
		switch e.Kind {
		case "text":
			textLines = append(textLines, e.Content)
		case "artifact":
			artifactLst = append(artifactLst, map[string]any{
				"path":   e.Path,
				"size":   e.Size,
				"sha256": e.SHA256,
				"mime":   e.Mime,
			})
		case "done":
			exitCode = e.ExitCode
			if e.Error != "" {
				runErr = e.Error
			}
		}
		mu.Unlock()

		// Send progress notification with a human-readable summary
		// so MCP clients (Claude Code etc) render it inline. Per
		// MCP spec the `message` field is meant for short scannable
		// status strings; previously we stuffed the full Event JSON
		// in there, which clients couldn't display nicely. Skip
		// events with no useful summary (e.g. claude_event, which
		// duplicates the text events we already emit).
		summary := progressSummary(e)
		if summary == "" {
			return
		}
		_ = srv.SendNotificationToClient(ctx, "notifications/progress", map[string]any{
			"progressToken": token,
			"progress":      seq,
			"message":       summary,
		})
	}

	// Always allocate a session root so artifacts survive teardown
	// AND /inputs/ can be bind-mounted (set at container create
	// time, can't be added to a running container). args.Output
	// overrides only where artifacts get pulled, not the session
	// root — inputs/ still lives under sessionRoot/.
	sessionRoot, artifactsDir, inputsDir, derr := newSessionDir()
	if derr != nil {
		return mcpgo.NewToolResultError("session dir: " + derr.Error()), nil
	}
	if _, err := stageInputs(args.Inputs, inputsDir); err != nil {
		return mcpgo.NewToolResultError("stage inputs: " + err.Error()), nil
	}
	outDir := args.Output
	if outDir == "" {
		outDir = artifactsDir
	}

	if err := prov.Run(ctx, agent.UpOpts{
		Image:         image,
		Harness:       hName,
		Workdir:       args.Workdir,
		Env:           envWithCb,
		Stream:        true,
		StreamHandler: handler,
		OutputDir:     outDir,
		InputsDir:     inputsDir,
	}, args.Prompt); err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}

	mu.Lock()
	defer mu.Unlock()
	// Resolve each streamed artifact event to its host path so the
	// caller can Read it directly, not just see its metadata.
	for _, a := range artifactLst {
		if path, ok := a["path"].(string); ok {
			a["host_path"] = filepath.Join(outDir, path)
		}
	}
	envelope := map[string]any{
		"text":          strings.Join(textLines, "\n"),
		"artifacts":     artifactLst,
		"exit_code":     exitCode,
		"artifacts_dir": outDir,
		"inputs_dir":    inputsDir,
		"session_dir":   sessionRoot,
	}
	if runErr != "" {
		envelope["error"] = runErr
	}
	body, _ := json.Marshal(envelope)
	return mcpgo.NewToolResultText(string(body)), nil
}

// ---- up --------------------------------------------------------

type upArgs struct {
	Harness string            `json:"harness,omitempty"`
	Workdir string            `json:"workdir,omitempty"`
	Name    string            `json:"name,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Image   string            `json:"image,omitempty"`
	Inputs  []string          `json:"inputs,omitempty"`
}

func addUpTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_up",
		mcpgo.WithDescription("Create a persistent agent session container. Returns the session id. Use `social_agent_exec` to run commands inside, `social_agent_pull` to fetch files from /artifacts, `social_agent_down` to tear down. For one-shot prompts use `social_agent_run` instead. `inputs` pre-stages files at /inputs (read-only) and `social_agent_upload` adds more files mid-session."),
		mcpgo.WithString("harness", mcpgo.Description("Coding-agent CLI to run inside (claude-code | echo). Default: claude-code.")),
		mcpgo.WithString("workdir", mcpgo.Description("Host path bind-mounted at /workspace. Default: no mount.")),
		mcpgo.WithString("name", mcpgo.Description("Explicit container name. Re-running `up` with the same name reuses the existing container.")),
		mcpgo.WithObject("env", mcpgo.Description("Additional env vars to set inside the container.")),
		mcpgo.WithString("image", mcpgo.Description("Override the docker image:tag.")),
		mcpgo.WithArray("inputs", mcpgo.Description("List of host file paths to copy into the session's inputs/ dir, bind-mounted read-only at /inputs in the container. Files only — directories are rejected. Add more files later via social_agent_upload. Items: type=string.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args upArgs) (*mcpgo.CallToolResult, error) {
		prov := buildProvider()
		image := args.Image
		if image == "" {
			image = resolveImage(cfg)
		}

		// Always allocate a session root + bind-mount /inputs/ so
		// social_agent_upload can later drop files into the same
		// host dir and have them appear inside the container
		// without needing docker cp. Stage caller-supplied paths
		// up front.
		sessionRoot, _, inputsDir, dirErr := newSessionDir()
		if dirErr != nil {
			return mcpgo.NewToolResultError("session dir: " + dirErr.Error()), nil
		}
		if _, err := stageInputs(args.Inputs, inputsDir); err != nil {
			return mcpgo.NewToolResultError("stage inputs: " + err.Error()), nil
		}

		sess, err := prov.Up(ctx, agent.UpOpts{
			Image:     image,
			Harness:   args.Harness,
			Workdir:   args.Workdir,
			Name:      args.Name,
			Env:       args.Env,
			InputsDir: inputsDir,
		})
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		// Remember the inputs dir per session so social_agent_upload
		// can find it without scanning. Container labels would also
		// work but in-process map is simpler and sessions are
		// process-lifetime anyway.
		sessionInputs.Store(sess.ID, inputsDir)
		body, _ := json.Marshal(map[string]any{
			"id":            sess.ID,
			"harness":       sess.Harness,
			"workdir":       sess.Workdir,
			"image":         sess.Image,
			"artifacts_url": sess.ArtifactsURL,
			"session_dir":   sessionRoot,
			"inputs_dir":    inputsDir,
		})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// sessionInputs maps session IDs (docker container IDs) to their
// host-side inputs dir so social_agent_upload can find where to
// drop new files. Populated by addUpTool on Up; survives until
// the daemon process exits or the session is removed via Down.
var sessionInputs sync.Map // string → string

// ---- exec ------------------------------------------------------

type execArgs struct {
	ID  string   `json:"id"`
	Cmd []string `json:"cmd"`
}

func addExecTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_exec",
		mcpgo.WithDescription("Run a command inside an existing agent session. Streams stdin/stdout/stderr through the call. Empty `cmd` runs the harness's interactive form (claude-code's `claude`); use a non-empty argv to run an arbitrary command."),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Session id from `social_agent_up`.")),
		mcpgo.WithArray("cmd", mcpgo.Description("Argv array. Empty = the harness's interactive form.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args execArgs) (*mcpgo.CallToolResult, error) {
		if strings.TrimSpace(args.ID) == "" {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		prov := buildProvider()
		var stdout, stderr bytes.Buffer
		if err := prov.Exec(ctx, args.ID, agent.ExecOpts{
			Cmd:    args.Cmd,
			Stdout: &stdout,
			Stderr: &stderr,
		}); err != nil {
			msg := fmt.Sprintf("exec: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			return mcpgo.NewToolResultError(msg), nil
		}
		body, _ := json.Marshal(map[string]any{
			"stdout": stdout.String(),
			"stderr": stderr.String(),
		})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- ls --------------------------------------------------------

func addLsTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_ls",
		mcpgo.WithDescription("List active agent sessions. Returns id, state, harness, image, workdir for each."),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, _ struct{}) (*mcpgo.CallToolResult, error) {
		prov := buildProvider()
		sessions, err := prov.List(ctx)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body, _ := json.Marshal(sessions)
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- down ------------------------------------------------------

type downArgs struct {
	IDs []string `json:"ids,omitempty"`
}

func addDownTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_down",
		mcpgo.WithDescription("Tear down agent sessions. Empty `ids` removes every session this binary created. Returns the list of removed ids."),
		mcpgo.WithArray("ids", mcpgo.Description("Session ids to remove. Empty = all of ours.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args downArgs) (*mcpgo.CallToolResult, error) {
		prov := buildProvider()
		// Resolve "all of ours" to explicit ids so the response
		// can list what was removed.
		ids := args.IDs
		if len(ids) == 0 {
			sessions, err := prov.List(ctx)
			if err != nil {
				return mcpgo.NewToolResultError(err.Error()), nil
			}
			for _, s := range sessions {
				ids = append(ids, s.ID)
			}
		}
		if err := prov.Down(ctx, ids...); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body, _ := json.Marshal(map[string]any{"removed": ids})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- pull ------------------------------------------------------

type pullArgs struct {
	ID   string `json:"id"`
	Path string `json:"path,omitempty"`
	To   string `json:"to,omitempty"`
}

func addPullTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_pull",
		mcpgo.WithDescription("Pull files from a session's /artifacts to the host. Empty `path` pulls the whole tree to `to` (or cwd). Non-empty `path` pulls one file. The agent inside the session writes returnable files to /artifacts/<name>; this tool downloads them."),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Session id.")),
		mcpgo.WithString("path", mcpgo.Description("Single-file path relative to /artifacts. Empty = whole tree.")),
		mcpgo.WithString("to", mcpgo.Description("Destination dir (whole tree) or file (single path). Default: cwd.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args pullArgs) (*mcpgo.CallToolResult, error) {
		if strings.TrimSpace(args.ID) == "" {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		prov := buildProvider()
		sessions, err := prov.List(ctx)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		var url string
		for _, sess := range sessions {
			if sess.ID == args.ID || hasIDPrefix(sess.ID, args.ID) || hasIDPrefix(args.ID, sess.ID) {
				url = sess.ArtifactsURL
				break
			}
		}
		if url == "" {
			return mcpgo.NewToolResultError(fmt.Sprintf("no reachable artifacts URL for session %q", args.ID)), nil
		}
		c := &artifacts.Client{BaseURL: url}
		dest := args.To
		if dest == "" {
			dest = "."
		}
		if args.Path != "" {
			dst := dest
			if dest == "." {
				dst = args.Path
			}
			if err := c.GetTo(ctx, args.Path, dst); err != nil {
				return mcpgo.NewToolResultError(err.Error()), nil
			}
			body, _ := json.Marshal(map[string]any{"pulled": []string{dst}})
			return mcpgo.NewToolResultText(string(body)), nil
		}
		count, bytes, err := c.PullAll(ctx, dest)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body, _ := json.Marshal(map[string]any{
			"count": count,
			"bytes": bytes,
			"to":    dest,
		})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- upload ----------------------------------------------------

type uploadArgs struct {
	ID     string   `json:"id"`
	Inputs []string `json:"inputs"`
}

func addUploadTool(s *server.MCPServer, _ Config) {
	tool := mcpgo.NewTool("social_agent_upload",
		mcpgo.WithDescription("Copy host files into a running session's /inputs/ dir. Mid-session counterpart to social_agent_pull: pre-stage files at session creation via social_agent_up({inputs:[…]}), or drop more in later via this tool. Files appear inside the container at /inputs/<basename> immediately (the dir is bind-mounted, so a host write is visible without a docker round-trip). Files only — directories rejected. Useful when the operator gathers more material partway through a multi-step session."),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Session id from `social_agent_up`. Prefix match works.")),
		mcpgo.WithArray("inputs", mcpgo.Description("Host file paths to copy into /inputs/. Items: type=string. Each lands at /inputs/<basename>.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args uploadArgs) (*mcpgo.CallToolResult, error) {
		if strings.TrimSpace(args.ID) == "" {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		if len(args.Inputs) == 0 {
			return mcpgo.NewToolResultError("inputs is required (list of host file paths)"), nil
		}
		// Resolve session id (prefix match) to its host inputs dir.
		var inputsDir string
		sessionInputs.Range(func(k, v any) bool {
			id, _ := k.(string)
			dir, _ := v.(string)
			if id == args.ID || hasIDPrefix(id, args.ID) || hasIDPrefix(args.ID, id) {
				inputsDir = dir
				return false
			}
			return true
		})
		if inputsDir == "" {
			return mcpgo.NewToolResultError(fmt.Sprintf("no inputs dir tracked for session %q (was the session created with social_agent_up after the upload feature shipped?)", args.ID)), nil
		}
		staged, err := stageInputs(args.Inputs, inputsDir)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body, _ := json.Marshal(map[string]any{
			"uploaded":   staged,
			"inputs_dir": inputsDir,
		})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- rm-file ---------------------------------------------------

type rmFileArgs struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

func addRmFileTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_rm_file",
		mcpgo.WithDescription("Remove one file from a session's /artifacts. Useful when iterating: prune stale outputs before re-running the agent."),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Session id.")),
		mcpgo.WithString("path", mcpgo.Required(), mcpgo.Description("File path relative to /artifacts.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args rmFileArgs) (*mcpgo.CallToolResult, error) {
		prov := buildProvider()
		sessions, err := prov.List(ctx)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		var url string
		for _, sess := range sessions {
			if sess.ID == args.ID || hasIDPrefix(sess.ID, args.ID) || hasIDPrefix(args.ID, sess.ID) {
				url = sess.ArtifactsURL
				break
			}
		}
		if url == "" {
			return mcpgo.NewToolResultError(fmt.Sprintf("no reachable artifacts URL for session %q", args.ID)), nil
		}
		c := &artifacts.Client{BaseURL: url}
		if err := c.Delete(ctx, args.Path); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText(`{"removed":true}`), nil
	}))
}

// ---- harness list ----------------------------------------------

func addHarnessListTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_harness_list",
		mcpgo.WithDescription("List the coding-agent CLIs (\"harnesses\") this binary supports — claude-code, echo, etc. echo is auth-free, useful for smoke tests."),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(_ context.Context, _ mcpgo.CallToolRequest, _ struct{}) (*mcpgo.CallToolResult, error) {
		body, _ := json.Marshal(harness.Names())
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- probe client ---------------------------------------------
//
// Diagnostic tool used to discover what bidirectional MCP
// surfaces the connected client supports. The connected client
// (Claude Code, Claude Desktop, claude.ai) declares its
// capabilities at initialize time — we cache those via the
// AfterInitialize hook above. This tool reports the cached
// capabilities AND attempts a real elicitation/sampling call so
// the operator can see whether the server-to-client request
// path actually round-trips.
//
// Intended as a one-shot probe during the bidirectional-input
// design phase — not part of the durable MCP API. Drop the tool
// (or guard it behind a build tag) once we've decided which
// surface to build on.

type probeClientArgs struct {
	// TrySampling, when true, also attempts a sampling/createMessage
	// request alongside the elicitation. Default false because
	// sampling triggers an LLM call on the client and costs tokens.
	TrySampling bool `json:"try_sampling,omitempty"`
}

func addProbeClientTool(s *server.MCPServer, _ Config) {
	tool := mcpgo.NewTool("social_agent_probe_client",
		mcpgo.WithDescription("Diagnostic: report the connected MCP client's declared capabilities (sampling, elicitation, roots) and attempt an elicitation/create round-trip. Used to figure out what bidirectional input surfaces are available before designing the inner-claude-asks-outer-client path. Set try_sampling=true to also test sampling/createMessage (warning: triggers an LLM call on the client)."),
		mcpgo.WithBoolean("try_sampling", mcpgo.Description("Also attempt a sampling/createMessage request. Default false (no token spend).")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args probeClientArgs) (*mcpgo.CallToolResult, error) {
		report := map[string]any{}

		// 1. Cached client capabilities.
		if caps := clientCaps.Load(); caps != nil {
			report["capabilities"] = map[string]any{
				"sampling":    caps.Sampling != nil,
				"elicitation": caps.Elicitation != nil,
				"roots":       caps.Roots != nil,
			}
		} else {
			report["capabilities"] = "(not captured — initialize hook didn't fire?)"
		}

		// 2. Try an elicitation/create request — server asks
		//    client for one short string. If the client doesn't
		//    support elicitation, mcp-go returns
		//    ErrElicitationNotSupported; otherwise we get the
		//    user's answer (or a decline).
		elicReq := mcpgo.ElicitationRequest{
			Request: mcpgo.Request{Method: string(mcpgo.MethodElicitationCreate)},
			Params: mcpgo.ElicitationParams{
				Message: "social-agent probe: please type any short string to confirm bidirectional input works.",
				RequestedSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"answer": map[string]any{
							"type":        "string",
							"description": "anything",
						},
					},
					"required": []string{"answer"},
				},
			},
		}
		if elicResult, err := s.RequestElicitation(ctx, elicReq); err != nil {
			report["elicitation"] = map[string]any{"error": err.Error()}
		} else {
			report["elicitation"] = map[string]any{
				"action":  elicResult.Action,
				"content": elicResult.Content,
			}
		}

		// 3. Optional sampling/createMessage probe.
		if args.TrySampling {
			sampReq := mcpgo.CreateMessageRequest{
				Request: mcpgo.Request{Method: string(mcpgo.MethodSamplingCreateMessage)},
				CreateMessageParams: mcpgo.CreateMessageParams{
					Messages: []mcpgo.SamplingMessage{
						{
							Role: mcpgo.RoleUser,
							Content: mcpgo.TextContent{
								Type: "text",
								Text: "Reply with the single word: pong",
							},
						},
					},
					MaxTokens: 16,
				},
			}
			if sampResult, err := s.RequestSampling(ctx, sampReq); err != nil {
				report["sampling"] = map[string]any{"error": err.Error()}
			} else {
				report["sampling"] = map[string]any{
					"role":    sampResult.Role,
					"content": sampResult.Content,
					"model":   sampResult.Model,
				}
			}
		}

		body, _ := json.Marshal(report)
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// hasIDPrefix is a tiny helper for short-form id resolution.
// Mirrors what cmd/social-agent/cmd_pull.go does.
func hasIDPrefix(s, p string) bool {
	if len(p) < 8 || len(s) < len(p) {
		return false
	}
	return s[:len(p)] == p
}
