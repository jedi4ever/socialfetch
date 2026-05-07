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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/agent"
	"github.com/jedi4ever/social-skills/internal/agent/artifacts"
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
// subdirs. Used by the legacy session-keyed code paths
// (addUpTool / addRunTool's pre-workspace flow). Kept for the
// streaming run path that still wants per-call isolation.
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

// sessionsRoot returns the parent dir holding every session's
// workspace. Lives at $TMPDIR/social-agent/sessions/. Each
// session's id is a 64-char hex string and its dir layout is
// <session_id>/{inputs,artifacts}/. The id is the access-control
// capability — whoever holds it can read/write that session's
// files. State on disk persists across server restarts so a
// caller who held a session_id before the restart can resume.
func sessionsRoot() string {
	return filepath.Join(os.TempDir(), "social-agent", "sessions")
}

// ArtifactsURLPrefix is the URL path prefix the artifacts file
// server lives at. Exported because the URL is part of the
// public MCP contract: `social_agent_list_artifacts` returns
// `<prefix>/<session_id>/<path>` per entry, and the operator
// hosting the MCP server registers the same prefix on their HTTP
// router (see cmd/social-agent/cmd_mcp.go's wiring of
// NewArtifactsHandler under mcphttp.Options.ExtraHandlers).
const ArtifactsURLPrefix = "/artifacts/"

// NewArtifactsHandler returns an http.Handler that serves
// $TMPDIR/social-agent/sessions/<session_id>/artifacts/<path> in
// response to GET <ArtifactsURLPrefix><session_id>/<path>. Used as
// a sidecar to the MCP server so callers can pull files via plain
// HTTP — saves round-trips vs. a per-file MCP `download` and
// dodges the protocol's message-size budget.
//
// Auth: the handler itself is unauthenticated; cmd_mcp.go wraps
// it in the same bearer-token gate the /mcp endpoint uses, so
// callers present `Authorization: Bearer $MCP_AUTH_TOKEN` exactly
// as for MCP. Together with the 256-bit unguessable session_id,
// that's two factors a remote attacker has to bypass: knowing the
// shared MCP token AND knowing a session_id that was never
// transmitted to them.
//
// Path validation: validSessionID rejects anything but 64-char
// hex (no path traversal); the rest of the path is resolved
// against the session's artifacts dir via safeWorkspacePath which
// rejects `..` and absolute paths. ServeFile follows symlinks —
// we don't create any inside the workspace, but a hostile agent
// could; followed symlinks must still resolve under the
// artifactsDir or http.ServeFile rejects them as forbidden.
func NewArtifactsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Strip the prefix; expect <session_id>/<rel_path>
		rest := strings.TrimPrefix(r.URL.Path, ArtifactsURLPrefix)
		if rest == r.URL.Path { // no prefix → not our path
			http.NotFound(w, r)
			return
		}
		i := strings.IndexByte(rest, '/')
		if i <= 0 {
			http.Error(w, "expected /artifacts/<session_id>/<path>", http.StatusBadRequest)
			return
		}
		sessionID, rel := rest[:i], rest[i+1:]
		if !validSessionID(sessionID) {
			http.Error(w, "invalid session_id", http.StatusBadRequest)
			return
		}
		if rel == "" {
			http.Error(w, "missing artifact path", http.StatusBadRequest)
			return
		}
		_, artifactsDir, err := sessionDirs(sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		abs, err := safeWorkspacePath(artifactsDir, rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		// Final containment check after symlink resolution —
		// belt-and-suspenders against a malicious agent dropping
		// a symlink to /etc/passwd in /artifacts.
		realArtifactsDir, _ := filepath.EvalSymlinks(artifactsDir)
		realAbs, err := filepath.EvalSymlinks(abs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if realArtifactsDir != "" && !strings.HasPrefix(realAbs, realArtifactsDir+string(filepath.Separator)) && realAbs != realArtifactsDir {
			http.Error(w, "path escapes session", http.StatusForbidden)
			return
		}
		http.ServeFile(w, r, abs)
	})
}

// artifactURL builds the relative URL for one artifact, suitable
// for embedding in an MCP response. Caller appends to whatever
// base URL they're using to reach the MCP server.
func artifactURL(sessionID, relPath string) string {
	return ArtifactsURLPrefix + sessionID + "/" + filepath.ToSlash(relPath)
}

// newSessionID returns a 64-char hex (256 bits of randomness) id
// suitable as a capability token. No timestamp prefix — sessions
// can outlive the server, and a stale timestamp would be
// misleading rather than helpful.
func newSessionID() string {
	var rand32 [32]byte
	_, _ = rand.Read(rand32[:])
	return hex.EncodeToString(rand32[:])
}

// validSessionID rejects anything that isn't a 64-char hex string.
// Belt-and-suspenders defence against path traversal — if the
// session_id reaches filepath.Join, we want to be certain it can't
// contain `..`, slashes, or anything else that escapes
// sessionsRoot.
func validSessionID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// sessionDirs returns the inputs+artifacts paths for an existing
// session. Returns an error if session_id is malformed or the dir
// doesn't exist (caller didn't run session_create, OR the id is
// guessed/leaked from another tenant).
func sessionDirs(sessionID string) (inputs, artifacts string, err error) {
	if !validSessionID(sessionID) {
		return "", "", fmt.Errorf("invalid session_id")
	}
	root := filepath.Join(sessionsRoot(), sessionID)
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return "", "", fmt.Errorf("no such session")
	}
	return filepath.Join(root, "inputs"), filepath.Join(root, "artifacts"), nil
}

// createSessionDirs creates a fresh per-session workspace.
// session_create is the only caller — every other tool uses
// sessionDirs which requires an existing dir.
func createSessionDirs(sessionID string) (inputs, artifacts string, err error) {
	root := filepath.Join(sessionsRoot(), sessionID)
	inputs = filepath.Join(root, "inputs")
	artifacts = filepath.Join(root, "artifacts")
	for _, d := range []string{inputs, artifacts} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", "", fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return inputs, artifacts, nil
}

// ensureFreshArtifacts wipes the contents of the workspace
// artifacts dir (preserving the dir itself). Called before each
// `run` so claude's container starts with an empty /artifacts and
// the caller's `list_artifacts` after the run reflects only what
// THIS run produced. Errors are returned but non-fatal upstream —
// run will surface them as a tool error rather than proceeding
// with stale state.
func ensureFreshArtifacts(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Dir doesn't exist → nothing to wipe; mkdir handled by
		// workspaceDirs.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
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

	// HTTPMode toggles which read-handle `list_artifacts` returns
	// per entry:
	//
	//   true  (HTTP transport)  → `url` field, a relative path
	//                             on the same host as /mcp; caller
	//                             fetches with HTTP + the same
	//                             Bearer token.
	//   false (stdio transport) → `host_path` field, the absolute
	//                             host filesystem path; caller is
	//                             on the same host so reads
	//                             directly with built-in file
	//                             tools.
	//
	// Set by the launcher in cmd/social-agent/cmd_mcp.go: HTTP mode
	// (--http <addr>) sets it true, stdio mode leaves it false.
	// The two modes never share a process, so callers don't have
	// to handle both shapes in one client.
	HTTPMode bool
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

// registerTools wires up the public MCP surface. Seven primitives,
// all keyed off a `session_id` the caller obtains from
// `session_create` and holds onto for the rest of their work:
//
//	social_agent_session_create      — open a fresh workspace, returns session_id
//	social_agent_session_close       — discard a workspace + its files
//	social_agent_run                 — start a prompt in a session (async, returns run_id)
//	social_agent_run_status          — poll a run by id
//	social_agent_upload_artifacts    — provide files to the agent (per session)
//	social_agent_download_artifacts  — read a file the agent produced (per session)
//	social_agent_list_artifacts      — see what's available to download (per session)
//
// Multi-tenant by design — concurrent callers each create their
// own session, get their own files dir, and run their own
// prompts in parallel. session_id is a 256-bit random capability:
// a caller without it can't see another caller's data, even if
// they connect to the same MCP server.
//
// State persists on disk under $TMPDIR/social-agent/sessions/<id>/
// so a caller who held a session_id before a server restart can
// resume — uploads + previous artifacts are still there. Only
// in-flight run state (the goroutine) is lost on restart.
//
// `run` is async because complex prompts can take many minutes,
// well past most MCP clients' default tool-call timeout (~60s in
// Claude Code). `run` returns immediately with a run_id and the
// caller polls `run_status` — every poll is a sub-second call so
// no timeout fires regardless of total wall-time. Each session
// has at most one run in flight; a second `run(session_id)` while
// the previous is still running returns `{busy: true}`. Different
// sessions run in parallel.
//
// Deliberately abstract — descriptions and response shapes hide
// containers, harnesses, host paths, and every other
// implementation detail. Operator-facing controls (image
// override, env-var injection, host workdir mounts, debug probes)
// live on the host CLI, not on MCP.
func registerTools(s *server.MCPServer, cfg Config) {
	addSessionCreateTool(s, cfg)
	addSessionCloseTool(s, cfg)
	addRunTool(s, cfg)
	addRunStatusTool(s, cfg)
	addUploadTool(s, cfg)
	// download_artifacts is only useful in stdio mode, where the
	// MCP transcript is the only channel back to the client. In
	// HTTP mode the same listener serves /artifacts/<session>/<path>
	// behind the bearer token, so list_artifacts returns a `url`
	// the client GETs directly — bypassing the MCP message-size
	// budget and parallelisable. Skip the tool to avoid offering
	// two ways to do the same thing (and to discourage the slow
	// path).
	if !cfg.HTTPMode {
		addDownloadTool(s, cfg)
	}
	addLsArtifactsTool(s, cfg)
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

// ---- session_create / session_close ---------------------------

// session_create has no args. session_close takes the session_id
// the caller wants to discard.
type sessionCloseArgs struct {
	SessionID string `json:"session_id"`
}

func addSessionCreateTool(s *server.MCPServer, _ Config) {
	tool := mcpgo.NewTool("social_agent_session_create",
		mcpgo.WithDescription("Open a new agent session. Returns `{session_id}` — a 256-bit capability token the caller stores and passes to every subsequent tool call (run, upload_artifacts, download_artifacts, list_artifacts, session_close). Sessions are isolated workspaces: only a caller holding the session_id can read or write that session's files. State persists on disk across server restarts so a caller who saved their session_id can resume after a restart. Call `social_agent_session_close` when done to free the workspace; otherwise it's left in place until the operator GCs it manually."),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(_ context.Context, _ mcpgo.CallToolRequest, _ struct{}) (*mcpgo.CallToolResult, error) {
		id := newSessionID()
		if _, _, err := createSessionDirs(id); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body, _ := json.Marshal(map[string]any{"session_id": id})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

func addSessionCloseTool(s *server.MCPServer, _ Config) {
	tool := mcpgo.NewTool("social_agent_session_close",
		mcpgo.WithDescription("Discard a session's workspace and any uploaded files / artifacts in it. Refused if a run is still in flight in this session — wait for the run to finish (poll its run_id) and try again. The session_id becomes invalid after a successful close."),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("Session id to close.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(_ context.Context, _ mcpgo.CallToolRequest, args sessionCloseArgs) (*mcpgo.CallToolResult, error) {
		if !validSessionID(args.SessionID) {
			return mcpgo.NewToolResultError("invalid session_id"), nil
		}
		registryMu.Lock()
		_, busy := currentBySession[args.SessionID]
		registryMu.Unlock()
		if busy {
			body, _ := json.Marshal(map[string]any{
				"error": "session has a run in flight; wait for it to finish before closing",
				"busy":  true,
			})
			return mcpgo.NewToolResultError(string(body)), nil
		}
		root := filepath.Join(sessionsRoot(), args.SessionID)
		// rm -rf the session's tree. validSessionID guarantees
		// the path is a 64-char hex segment under sessionsRoot —
		// no path-traversal risk.
		if err := os.RemoveAll(root); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body, _ := json.Marshal(map[string]any{"closed": args.SessionID})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- run (async) -----------------------------------------------
//
// The run pipeline is async to dodge MCP-client tool-call timeouts:
// `run` registers a runRecord, kicks the work off in a goroutine,
// and returns the run_id immediately. The caller polls
// `run_status` until status flips to "done" or "error" and reads
// the response/artifacts from there.
//
// One run executes at a time. A second `run` while the first is
// in-flight returns an error pointing at the existing run_id.
// Workspace state is shared (inputs/ + artifacts/), so concurrent
// runs would clobber each other — serialization keeps the model
// predictable.
//
// runRecord is in-memory only; restarting the MCP server forgets
// every record. Acceptable for single-process v1; revisit when we
// add persistence for crash recovery.

const (
	runStatusRunning = "running"
	runStatusDone    = "done"
	runStatusError   = "error"
)

// runRecord is the per-run state surfaced via run_status. Every
// access goes through registryMu — including reads — so the
// goroutine writing the final result can't race against a
// concurrent poll. SessionID is the workspace this run wrote to;
// the caller's `list_artifacts(session_id)` after the run reads
// from there.
type runRecord struct {
	ID         string
	SessionID  string
	Status     string
	Prompt     string
	StartedAt  time.Time
	FinishedAt time.Time
	Response   string
	Artifacts  []string
	Error      string
	ExitCode   int
	done       chan struct{} // closed when status flips to done|error
}

// snapshot copies the public fields into a JSON-shaped map for the
// MCP response. Caller must hold registryMu.
func (r *runRecord) snapshot() map[string]any {
	out := map[string]any{
		"run_id":     r.ID,
		"status":     r.Status,
		"started_at": r.StartedAt.Format(time.RFC3339Nano),
	}
	if !r.FinishedAt.IsZero() {
		out["finished_at"] = r.FinishedAt.Format(time.RFC3339Nano)
		out["duration_seconds"] = r.FinishedAt.Sub(r.StartedAt).Seconds()
	}
	switch r.Status {
	case runStatusDone:
		out["response"] = r.Response
		out["artifacts"] = r.Artifacts
		out["exit_code"] = r.ExitCode
	case runStatusError:
		out["error"] = r.Error
		// Include partial response/artifacts if we got that far —
		// helpful for debugging, no harm if both are zero.
		if r.Response != "" {
			out["response"] = r.Response
		}
		if len(r.Artifacts) > 0 {
			out["artifacts"] = r.Artifacts
		}
	}
	return out
}

// Registry: every active run keyed by run_id, plus the current
// in-flight run per session_id. registryMu serializes all reads
// and writes — both the map and the runRecord fields. Per-session
// in-flight tracking gates concurrent runs in the same session
// without blocking different sessions from running in parallel.
var (
	registryMu       sync.Mutex
	runs             = map[string]*runRecord{}
	currentBySession = map[string]*runRecord{}
)

// newRunID returns an unguessable run identifier — 256 bits of
// randomness (64 hex chars) prefixed by a sortable timestamp for
// human debugging. The timestamp isn't secret; the random tail is
// what stops brute-force.
//
// run_id is a *capability token*: any caller holding it can poll
// `run_status` and read the agent's prompt + response + artifacts.
// run_id is returned only to the caller who invoked `run`. The
// "busy" error from a concurrent `run` in the same session
// deliberately does NOT include the in-flight run_id (that would
// hand one caller access to another's run).
func newRunID() string {
	var rand32 [32]byte // 256 bits
	_, _ = rand.Read(rand32[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(rand32[:])
}

type runArgs struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

func addRunTool(s *server.MCPServer, cfg Config) {
	tool := mcpgo.NewTool("social_agent_run",
		mcpgo.WithDescription("Start the agent on a prompt within a session. Returns immediately with a `run_id` — poll `social_agent_run_status` with that id until `status` is `done` or `error` to get the agent's response. The `run_id` is the only way to reach this run's status, prompt, or output, so keep it; treat it like a password. Files the agent produces are recorded as artifacts inside the session; use `social_agent_list_artifacts` and `social_agent_download_artifacts` to read them. Files staged via `social_agent_upload_artifacts` are available to the agent during its run. Each session has at most one run in flight; a second `run` in the same session returns `{busy: true}`. Different sessions run in parallel."),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("Session id from `social_agent_session_create`.")),
		mcpgo.WithString("prompt", mcpgo.Required(), mcpgo.Description("The prompt for the agent. Plain English.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(_ context.Context, _ mcpgo.CallToolRequest, args runArgs) (*mcpgo.CallToolResult, error) {
		prompt := strings.TrimSpace(args.Prompt)
		if prompt == "" {
			return mcpgo.NewToolResultError("prompt is required"), nil
		}
		// Validate the session exists on disk before claiming a
		// per-session run slot. Cheap fail-fast for the common
		// "wrong session_id" mistake.
		if _, _, err := sessionDirs(args.SessionID); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}

		registryMu.Lock()
		if _, busy := currentBySession[args.SessionID]; busy {
			// Don't include the in-flight run_id in the response
			// — run_id is a capability and leaking it would hand
			// access to another caller. The legitimate caller who
			// started that run already has its run_id; they
			// should poll that. A different caller hitting the
			// same session_id (rare; that means the session_id
			// itself leaked) just sees "busy".
			registryMu.Unlock()
			body, _ := json.Marshal(map[string]any{
				"error": "session has another run in flight; retry shortly",
				"busy":  true,
			})
			return mcpgo.NewToolResultError(string(body)), nil
		}
		rec := &runRecord{
			ID:        newRunID(),
			SessionID: args.SessionID,
			Status:    runStatusRunning,
			Prompt:    prompt,
			StartedAt: time.Now().UTC(),
			done:      make(chan struct{}),
		}
		runs[rec.ID] = rec
		currentBySession[args.SessionID] = rec
		registryMu.Unlock()

		// Background ctx — the MCP request ctx ends when this
		// handler returns, but the run lives on. The container's
		// own runtime is the only effective timeout; if the
		// operator wants to abort, kill the docker container by
		// hand from the host CLI.
		go executeRun(context.Background(), cfg, rec)

		body, _ := json.Marshal(map[string]any{
			"run_id": rec.ID,
			"status": rec.Status,
		})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// executeRun is the goroutine body that does the actual work. On
// completion it transitions the runRecord to done|error and frees
// the session's in-flight slot so a follow-up `run` in the same
// session can proceed. Always closes rec.done so any future
// long-poll variant can wait on it cheaply.
func executeRun(ctx context.Context, cfg Config, rec *runRecord) {
	defer func() {
		registryMu.Lock()
		if rec.Status == runStatusRunning {
			// Defensive: should be set explicitly below. Mark as
			// error to avoid leaving a record stuck "running".
			rec.Status = runStatusError
			if rec.Error == "" {
				rec.Error = "internal: run finished without setting terminal status"
			}
		}
		if rec.FinishedAt.IsZero() {
			rec.FinishedAt = time.Now().UTC()
		}
		if cur := currentBySession[rec.SessionID]; cur == rec {
			delete(currentBySession, rec.SessionID)
		}
		close(rec.done)
		registryMu.Unlock()
	}()

	finalize := func(status, errMsg, response string, artifacts []string, exitCode int) {
		registryMu.Lock()
		rec.Status = status
		rec.Error = errMsg
		rec.Response = response
		rec.Artifacts = artifacts
		rec.ExitCode = exitCode
		rec.FinishedAt = time.Now().UTC()
		registryMu.Unlock()
	}

	prov := buildProvider()
	image := resolveImage(cfg)
	hName := "claude-code"
	h, err := harness.Get(hName)
	if err != nil {
		finalize(runStatusError, err.Error(), "", nil, 0)
		return
	}

	inputsDir, artifactsDir, err := sessionDirs(rec.SessionID)
	if err != nil {
		finalize(runStatusError, "session: "+err.Error(), "", nil, 0)
		return
	}
	if err := ensureFreshArtifacts(artifactsDir); err != nil {
		finalize(runStatusError, "clear artifacts: "+err.Error(), "", nil, 0)
		return
	}

	sess, err := prov.Up(ctx, agent.UpOpts{
		Image:     image,
		Harness:   hName,
		InputsDir: inputsDir,
	})
	if err != nil {
		finalize(runStatusError, err.Error(), "", nil, 0)
		return
	}
	defer func() {
		_ = prov.Down(context.Background(), sess.ID)
	}()

	var stdout, stderr bytes.Buffer
	execErr := prov.Exec(ctx, sess.ID, agent.ExecOpts{
		Cmd:    h.InvokePrompt(rec.Prompt),
		Stdout: &stdout,
		Stderr: &stderr,
	})

	// Pull artifacts whether or not exec succeeded — claude may have
	// written partial output before the failure, useful for debugging.
	var relArtifacts []string
	if sess.ArtifactsURL != "" {
		c := &artifacts.Client{BaseURL: sess.ArtifactsURL}
		if entries, err := c.List(ctx); err == nil {
			for _, e := range entries {
				dst := filepath.Join(artifactsDir, e.Path)
				if err := c.GetTo(ctx, e.Path, dst); err != nil {
					continue
				}
				relArtifacts = append(relArtifacts, e.Path)
			}
		}
	}

	if execErr != nil {
		errMsg := fmt.Sprintf("exec: %v\nstderr: %s", execErr, strings.TrimSpace(stderr.String()))
		finalize(runStatusError, errMsg, stdout.String(), relArtifacts, 0)
		return
	}
	finalize(runStatusDone, "", stdout.String(), relArtifacts, 0)
}

// ---- run_status -------------------------------------------------

type runStatusArgs struct {
	RunID string `json:"run_id"`
}

func addRunStatusTool(s *server.MCPServer, _ Config) {
	tool := mcpgo.NewTool("social_agent_run_status",
		mcpgo.WithDescription("Check on a run started by `social_agent_run`. Returns `{run_id, status, started_at, finished_at?, duration_seconds?, response?, artifacts?, error?, exit_code?}` where status is one of `running`, `done`, `error`. When status is `done` the response and artifacts list are populated; when `error` the error field describes what went wrong (along with any partial response/artifacts). Poll this tool until status is no longer `running`."),
		mcpgo.WithString("run_id", mcpgo.Required(), mcpgo.Description("The run_id returned by `social_agent_run`.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(_ context.Context, _ mcpgo.CallToolRequest, args runStatusArgs) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(args.RunID)
		if id == "" {
			return mcpgo.NewToolResultError("run_id is required"), nil
		}
		registryMu.Lock()
		rec, ok := runs[id]
		var snap map[string]any
		if ok {
			snap = rec.snapshot()
		}
		registryMu.Unlock()
		if !ok {
			return mcpgo.NewToolResultError(fmt.Sprintf("no run with id %q", id)), nil
		}
		body, _ := json.Marshal(snap)
		return mcpgo.NewToolResultText(string(body)), nil
	}))
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
		mcpgo.WithDescription("Pull files from a session's /artifacts to the host filesystem (the host running this MCP server). Empty `path` pulls the whole tree to `to` (or cwd). Non-empty `path` pulls one file. NOTE: writes land on the MCP server's host — if you're calling from a different container or machine you can't see the result; use `social_agent_read` instead, which returns content directly in the MCP response."),
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

// ---- download -------------------------------------------------
//
// addDownloadTool / addUploadTool / addLsArtifactsTool form the
// public artifact surface — upload some files for the agent,
// run a prompt, list what came out, download what's interesting.
// All three operate on the persistent host workspace populated by
// `run`, so they keep working after the container behind a run is
// torn down. The tool descriptions deliberately avoid container /
// docker / filesystem-path vocabulary: a caller should be able to
// use this MCP without knowing there's a container behind the
// agent at all.
//
// Naming: upload_artifacts / download_artifacts are the symmetric
// pair, list_artifacts shows what's available. The internal split
// (uploads land in `inputs/`, downloads come from `artifacts/`) is
// hidden — callers see one bag of artifacts.
//
// Encoding: MCP text content can't carry arbitrary bytes (NULs,
// invalid UTF-8). isPrintableUTF8 sniffs the chunk; valid text
// goes back as `content`, anything else as `content_b64` (base64).
//
// Size cap: response is bounded at `max_bytes` (default 256 KB,
// hard cap 4 MB) so a giant artifact can't stuff the transcript or
// blow past the client's context budget. Pagination via
// `start_byte` + the returned `next_start` for files larger than
// the cap.

type downloadArgs struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Start     int64  `json:"start_byte,omitempty"`
	MaxBytes  int64  `json:"max_bytes,omitempty"`
}

func addDownloadTool(s *server.MCPServer, _ Config) {
	tool := mcpgo.NewTool("social_agent_download_artifacts",
		mcpgo.WithDescription("Read one artifact the agent has produced in the given session. Returns the file's content as text (`content`) or, if the data isn't valid UTF-8, as base64 (`content_b64`). Paginated for large files — up to `max_bytes` per call; resume with `start_byte` from the previous response's `next_start`. Call `social_agent_list_artifacts` first to see what's available."),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("Session id from `social_agent_session_create`.")),
		mcpgo.WithString("path", mcpgo.Required(), mcpgo.Description("Artifact name (as returned by `social_agent_list_artifacts`).")),
		mcpgo.WithNumber("start_byte", mcpgo.Description("Byte offset to start at (default 0). Use the previous response's `next_start` to page through.")),
		mcpgo.WithNumber("max_bytes", mcpgo.Description("Max bytes to return (default 262144, hard cap 4194304).")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args downloadArgs) (*mcpgo.CallToolResult, error) {
		if strings.TrimSpace(args.Path) == "" {
			return mcpgo.NewToolResultError("path is required"), nil
		}
		_, artifactsDir, err := sessionDirs(args.SessionID)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		abs, err := safeWorkspacePath(artifactsDir, args.Path)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		f, err := os.Open(abs)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		total := info.Size()
		start := args.Start
		if start < 0 {
			start = 0
		}
		if start > total {
			start = total
		}
		max := args.MaxBytes
		const defaultMax = int64(256 * 1024)
		const hardMax = int64(4 * 1024 * 1024)
		switch {
		case max <= 0:
			max = defaultMax
		case max > hardMax:
			max = hardMax
		}
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		buf := make([]byte, max)
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		chunk := buf[:n]
		nextStart := start + int64(n)
		out := map[string]any{
			"path":       args.Path,
			"size":       total,
			"start":      start,
			"bytes":      n,
			"next_start": nextStart,
			"eof":        nextStart >= total,
		}
		if isPrintableUTF8(chunk) {
			out["content"] = string(chunk)
			out["encoding"] = "utf8"
		} else {
			out["content_b64"] = base64StdEncode(chunk)
			out["encoding"] = "base64"
		}
		body, _ := json.Marshal(out)
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- upload ----------------------------------------------------

type uploadArgs struct {
	// SessionID names the workspace to drop the files into. Each
	// session has its own bag of inputs/artifacts.
	SessionID string `json:"session_id"`
	// Files is a list of host file paths the operator wants to
	// make available to the agent. The caller-visible field name
	// is `files` to match the tool's description vocabulary —
	// "upload these files" reads cleaner than "upload these
	// inputs", and the inputs/artifacts split is internal detail
	// the MCP surface deliberately doesn't expose.
	Files []string `json:"files"`
}

func addUploadTool(s *server.MCPServer, _ Config) {
	tool := mcpgo.NewTool("social_agent_upload_artifacts",
		mcpgo.WithDescription("Make files available to the agent in the given session. Each file is registered under its basename and stays available for subsequent runs in that session — upload once, run many prompts. Files only; directories are rejected. Returns the list of registered names."),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("Session id from `social_agent_session_create`.")),
		mcpgo.WithArray("files", mcpgo.Description("Host file paths to make available to the agent. Items: type=string. Each is registered under its basename; collisions overwrite.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(_ context.Context, _ mcpgo.CallToolRequest, args uploadArgs) (*mcpgo.CallToolResult, error) {
		if len(args.Files) == 0 {
			return mcpgo.NewToolResultError("files is required (list of host file paths)"), nil
		}
		inputsDir, _, err := sessionDirs(args.SessionID)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		staged, err := stageInputs(args.Files, inputsDir)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		// Strip the host inputs-dir prefix from each staged path
		// so the response shows only the basenames the caller can
		// reference — never the MCP server's filesystem layout.
		names := make([]string, 0, len(staged))
		for _, p := range staged {
			names = append(names, filepath.Base(p))
		}
		body, _ := json.Marshal(map[string]any{
			"uploaded": names,
		})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- list_artifacts -------------------------------------------

type listArtifactsArgs struct {
	SessionID string `json:"session_id"`
}

// listEntry is the wire shape for list_artifacts entries. `path`
// is always the artifact's name relative to the session's artifacts
// dir; one of `url`/`host_path` is set depending on transport so
// the caller knows how to actually read the bytes.
//
// HTTP transport → `url`: relative path on the same host as /mcp;
// fetch with HTTP + the same Bearer token. URL field omitted in
// stdio mode (no HTTP server is listening).
//
// stdio transport → `host_path`: absolute host filesystem path;
// caller is the parent process, on the same host, so it can read
// the file directly with built-in file tools. host_path omitted in
// HTTP mode (caller may be remote and shouldn't see host paths).
type listEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	URL      string `json:"url,omitempty"`
	HostPath string `json:"host_path,omitempty"`
}

func addLsArtifactsTool(s *server.MCPServer, cfg Config) {
	desc := "List artifacts the agent has produced in the given session. Each entry returns `{path, size, host_path}` — `host_path` is the absolute host filesystem path; read files directly with your built-in file tools. Falls back to `social_agent_download_artifacts` if you'd rather pull bytes through MCP."
	if cfg.HTTPMode {
		desc = "List artifacts the agent has produced in the given session. Each entry returns `{path, size, url}` — `url` is a relative path on the same host as this MCP endpoint that serves the file's bytes (with the same Authorization: Bearer token /mcp uses). Fetch URLs directly with HTTP for bulk/parallel downloads, or call `social_agent_download_artifacts` if your client can't make HTTP requests."
	}
	tool := mcpgo.NewTool("social_agent_list_artifacts",
		mcpgo.WithDescription(desc),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("Session id from `social_agent_session_create`.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(_ context.Context, _ mcpgo.CallToolRequest, args listArtifactsArgs) (*mcpgo.CallToolResult, error) {
		_, artifactsDir, err := sessionDirs(args.SessionID)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		raw, err := listArtifacts(artifactsDir)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		entries := make([]listEntry, 0, len(raw))
		for _, e := range raw {
			entry := listEntry{Path: e.Path, Size: e.Size}
			if cfg.HTTPMode {
				entry.URL = artifactURL(args.SessionID, e.Path)
			} else {
				entry.HostPath = filepath.Join(artifactsDir, filepath.FromSlash(e.Path))
			}
			entries = append(entries, entry)
		}
		body, _ := json.Marshal(map[string]any{
			"entries": entries,
		})
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// artifactEntry mirrors what the in-container artifacts HTTP
// server returns from GET /artifacts/, but populated by walking
// the host workspace tree instead. Path is relative to the
// workspace artifacts dir; Size is bytes.
type artifactEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// listArtifacts walks `root`, returning every regular file as an
// entry with a forward-slash relative path (filepath.ToSlash so
// macOS / Linux output matches and the cross-platform consumer
// doesn't see backslashes when we eventually run on Windows).
func listArtifacts(root string) ([]artifactEntry, error) {
	var out []artifactEntry
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, artifactEntry{
			Path: filepath.ToSlash(rel),
			Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// safeWorkspacePath joins root + rel and verifies the result stays
// inside root (defends against `../../etc/passwd`-style paths from
// a hostile MCP caller). Returns the absolute path on success.
func safeWorkspacePath(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	abs := filepath.Join(root, clean)
	// Belt-and-suspenders: ensure abs is still under root after
	// the join. Symlinks inside the workspace could redirect
	// elsewhere; we don't follow them at the resolve step but
	// open(2) will, so reject any abs that isn't a direct child.
	rootAbs, _ := filepath.Abs(root)
	absAbs, _ := filepath.Abs(abs)
	if !strings.HasPrefix(absAbs, rootAbs+string(filepath.Separator)) && absAbs != rootAbs {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return abs, nil
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

// isPrintableUTF8 reports whether b is valid UTF-8 with no NUL byte.
// social_agent_read uses this to decide between returning content as
// `content` (text) or `content_b64` (base64). NUL is technically
// valid UTF-8 but breaks downstream consumers that treat it as a
// string terminator, so we route NUL-bearing payloads through base64.
func isPrintableUTF8(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return true
}

// base64StdEncode is the trivial wrapper. Inline'd as a function
// rather than calling base64.StdEncoding.EncodeToString at every
// call site so the import surface stays explicit at this layer.
func base64StdEncode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
