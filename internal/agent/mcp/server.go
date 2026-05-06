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
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/agent"
	"github.com/jedi4ever/social-skills/internal/agent/artifacts"
	"github.com/jedi4ever/social-skills/internal/agent/harness"
	dockerprov "github.com/jedi4ever/social-skills/internal/agent/providers/docker"
	"github.com/jedi4ever/social-skills/internal/agent/streaming"
)

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
	s := server.NewMCPServer(
		"social-agent",
		cfg.Version,
		server.WithToolCapabilities(false),
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
	addRmFileTool(s, cfg)
	addHarnessListTool(s, cfg)
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
		sess, err := prov.Up(ctx, agent.UpOpts{
			Image:   image,
			Harness: hName,
			Workdir: args.Workdir,
			Env:     args.Env,
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

		// Pull artifacts when output is set.
		var pulledFiles []string
		if args.Output != "" && sess.ArtifactsURL != "" {
			c := &artifacts.Client{BaseURL: sess.ArtifactsURL}
			entries, err := c.List(ctx)
			if err == nil {
				for _, e := range entries {
					dst := args.Output + "/" + e.Path
					if err := c.GetTo(ctx, e.Path, dst); err != nil {
						continue
					}
					pulledFiles = append(pulledFiles, dst)
				}
			}
		}

		// Compose the response: claude's stdout + (when artifacts
		// were pulled) a list of paths the agent can read with
		// social_fetch_read_file or its native Read tool.
		envelope := map[string]any{
			"text":      stdout.String(),
			"artifacts": pulledFiles,
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

		// Send progress notification. params shape mirrors MCP
		// spec: progressToken + progress + (optional) total +
		// (optional) message. We tuck the full Event into the
		// notification so clients can do typed UI off it.
		body, _ := json.Marshal(e)
		_ = srv.SendNotificationToClient(ctx, "notifications/progress", map[string]any{
			"progressToken": token,
			"progress":      seq,
			"message":       string(body),
		})
	}

	if err := prov.Run(ctx, agent.UpOpts{
		Image:         image,
		Harness:       hName,
		Workdir:       args.Workdir,
		Env:           args.Env,
		Stream:        true,
		StreamHandler: handler,
	}, args.Prompt); err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}

	mu.Lock()
	defer mu.Unlock()
	envelope := map[string]any{
		"text":      strings.Join(textLines, "\n"),
		"artifacts": artifactLst,
		"exit_code": exitCode,
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
}

func addUpTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_up",
		mcpgo.WithDescription("Create a persistent agent session container. Returns the session id. Use `social_agent_exec` to run commands inside, `social_agent_pull` to fetch files from /artifacts, `social_agent_down` to tear down. For one-shot prompts use `social_agent_run` instead."),
		mcpgo.WithString("harness", mcpgo.Description("Coding-agent CLI to run inside (claude-code | echo). Default: claude-code.")),
		mcpgo.WithString("workdir", mcpgo.Description("Host path bind-mounted at /workspace. Default: no mount.")),
		mcpgo.WithString("name", mcpgo.Description("Explicit container name. Re-running `up` with the same name reuses the existing container.")),
		mcpgo.WithObject("env", mcpgo.Description("Additional env vars to set inside the container.")),
		mcpgo.WithString("image", mcpgo.Description("Override the docker image:tag.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args upArgs) (*mcpgo.CallToolResult, error) {
		prov := buildProvider()
		image := args.Image
		if image == "" {
			image = resolveImage(cfg)
		}
		s, err := prov.Up(ctx, agent.UpOpts{
			Image:   image,
			Harness: args.Harness,
			Workdir: args.Workdir,
			Name:    args.Name,
			Env:     args.Env,
		})
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body, _ := json.Marshal(map[string]any{
			"id":            s.ID,
			"harness":       s.Harness,
			"workdir":       s.Workdir,
			"image":         s.Image,
			"artifacts_url": s.ArtifactsURL,
		})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

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

// hasIDPrefix is a tiny helper for short-form id resolution.
// Mirrors what cmd/social-agent/cmd_pull.go does.
func hasIDPrefix(s, p string) bool {
	if len(p) < 8 || len(s) < len(p) {
		return false
	}
	return s[:len(p)] == p
}
