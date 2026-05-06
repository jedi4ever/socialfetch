// Package agent is the substrate for sandboxed coding-agent
// sessions. Mirrors the role internal/browser plays for chromedp
// pools: a Provider abstraction (where the agent runs — docker
// today, daytona later) plus a Harness abstraction (which coding
// agent runs inside — claude-code today, codex / gemini / etc
// later).
//
// Lives separately from internal/browser because the semantics
// differ: agent sessions are stateful, long-lived containers
// driven by an interactive child process, not stateless HTTP
// endpoints. Trying to share the Provider interface would only
// get awkward on `Exec` and `Run`.
package agent

import (
	"context"
	"io"
	"time"

	"github.com/jedi4ever/social-skills/internal/agent/streaming"
)

// Provider is the per-substrate runtime that creates and manages
// agent sessions. Each implementation handles its own
// reuse-existing logic, label filtering ("ours" vs "theirs"),
// and credential injection mechanics.
//
// Methods:
//
//   - Up creates a new agent session and returns the metadata.
//     The session stays alive until Down (or the substrate's own
//     reaper kicks in). The container is "warm" — entered via
//     Exec or hosted via Run.
//
//   - Down removes one or more sessions by ID. Empty ids = remove
//     every session this provider has labeled as ours.
//
//   - List enumerates the provider's currently-tracked sessions.
//     The Backend.State field reflects the substrate's view at
//     call time ("running" / "stopped" / "removed").
//
//   - Exec runs cmd inside an existing session, streaming
//     stdin/stdout/stderr through the supplied ExecOpts. Empty
//     cmd = the harness's interactive form (e.g. ["claude"]).
//
//   - Run is the one-shot path: Up + Exec(harness.InvokePrompt(p))
//
//   - capture output + Down. Implementations may special-case
//     this for speed (skip session bookkeeping, stream output
//     directly) but the externally-observable behaviour is the
//     compose of the three.
type Provider interface {
	Name() string
	Up(ctx context.Context, opts UpOpts) (*Session, error)
	Down(ctx context.Context, ids ...string) error
	List(ctx context.Context) ([]Session, error)
	Exec(ctx context.Context, id string, opts ExecOpts) error
	Run(ctx context.Context, opts UpOpts, prompt string) error
}

// Session is one running agent container. Provider-scoped ID;
// Provider, Harness, and Image identify how the session was
// stamped at Up time so List output is self-describing without
// extra lookups.
type Session struct {
	// ID is the substrate's identifier — for docker, the container
	// id. Stable across List calls until Down removes it.
	ID string

	// Provider is the substrate name ("docker", "daytona", …) so
	// /ls output across multiple providers can be merged later.
	Provider string

	// Harness is the agent CLI bundled into the session's image
	// ("claude-code" today). Read by the daemon's HTTP layer to
	// route /run requests to the right invocation form.
	Harness string

	// Image is the docker image:tag the session is running. Useful
	// when the operator wants to know which built version of
	// social-skills-agent a long-lived session picked up.
	Image string

	// Workdir is the host path bind-mounted at /workspace inside
	// the container, or "" when the session was started without
	// --workdir (no host filesystem access).
	Workdir string

	// Created is the wall-clock time Up returned the session.
	// Substrate may have its own creation timestamp — this one is
	// the social-agent-side stamp, useful for "how old is this
	// session" reporting in /ls.
	Created time.Time

	// State is the substrate's last-known status: "running",
	// "stopped", "removed". Updated by List.
	State string

	// Labels carries provider-specific metadata. The substrate's
	// "ours vs theirs" filter label is in here too.
	Labels map[string]string

	// ArtifactsURL is where the operator-side artifacts client
	// can reach the in-container `social-agent artifacts serve`
	// HTTP server. Substrate-specific resolution: local docker
	// uses `http://127.0.0.1:<host-port>` (read back via
	// `docker port <id> 5563`); daytona will use the sandbox's
	// preview URL. Empty when the provider didn't publish the
	// port (e.g. older sessions created before this field
	// existed) — `social-agent pull <id>` errors with a clear
	// message in that case.
	ArtifactsURL string
}

// UpOpts shapes the session-creation request. All fields are
// optional unless noted; sane defaults come from the chosen
// provider + harness.
type UpOpts struct {
	// Image is the docker image:tag to run. Empty = the provider's
	// default ("social-skills-agent:<Version>").
	Image string

	// Harness names the coding-agent CLI inside the image.
	// Default: "claude-code". Lookup happens via
	// internal/agent/harness.Get.
	Harness string

	// Workdir is the host path to bind-mount at /workspace. Empty
	// = no mount (the container has no host filesystem access).
	// Operator opts in via `--workdir DIR` on the CLI; safer
	// default for an agent that can run shell commands.
	Workdir string

	// Name is an explicit container name. Empty = auto-generated.
	// Setting a name makes `up` idempotent: a second `up` with the
	// same name reuses the existing container if it's still up.
	Name string

	// Env is a key/value map of additional env vars to set in the
	// container — on top of whatever the harness's EnvFromHost
	// selected. Caller-supplied entries win on collisions.
	Env map[string]string

	// Labels is provider-specific metadata stamped on the session
	// at Up time (docker labels, daytona labels, …). The substrate
	// adds its own filter label automatically.
	Labels map[string]string

	// CredentialsBlob is base64 of the harness's auth payload (for
	// claude-code: the OAuth credentials JSON). When set, the
	// container's entrypoint decodes it into the harness's
	// canonical creds path. Ignored when the harness's
	// EnvFromHost already returned an API-key env var.
	CredentialsBlob string

	// OutputDir, when set, asks Provider.Run to pull /artifacts
	// from the session into this host directory after Exec
	// completes and before Down. Empty = skip the pull. Ignored
	// by Up (it's a one-shot-Run concern; the equivalent for
	// persistent sessions is `social-agent pull <id>` on demand).
	OutputDir string

	// InputsDir is a host directory bind-mounted read-only at
	// /inputs in the container. Used to pre-stage operator-supplied
	// files the agent should work on (e.g. a PDF to summarise, a
	// notes.md to extend). Mirrors OutputDir as the inbound
	// counterpart of /artifacts. Empty = no /inputs mount.
	//
	// The MCP layer copies caller-supplied paths into a per-session
	// staging dir before passing it here, so the agent never sees
	// the operator's real filesystem layout — only the curated
	// subset under /inputs/.
	InputsDir string

	// Stream switches Provider.Run from "print claude's response
	// + post-run pull" to "emit JSONL events on stdout as the run
	// progresses": session up/down, line-buffered text, artifact
	// notifications, terminating done. Useful for long-running
	// agent prompts where the operator (or a parent agent) wants
	// progressive feedback. Ignored by Up. See
	// internal/agent/streaming for the event shape.
	Stream bool

	// StreamHandler, when non-nil and Stream is true, replaces the
	// default JSONL-on-stdout sink with this callback — every
	// event the run produces is delivered here instead. Used by the
	// MCP server to convert events into progress notifications, and
	// by future in-process consumers (the daemon's HTTP/SSE
	// surface, parent-agent hooks, etc). Calls are serialised by
	// runStream's mutex; the handler may panic-safe but should not
	// block long — events are produced from a 1s artifact poll loop
	// and a line-buffered stdout reader.
	StreamHandler func(streaming.Event)
}

// ExecOpts wires stdin/stdout/stderr from the caller through to
// the in-container command. Used by both `social-agent exec` (PTY
// shell) and `social-agent run` (one-shot prompt with captured
// output).
type ExecOpts struct {
	// Cmd is what to run inside the container. Empty = the
	// harness's interactive form (e.g. ["claude"]).
	Cmd []string

	// Stdin / Stdout / Stderr are the streams to wire through.
	// nil for any of them = /dev/null on that fd. Provider may
	// allocate a PTY when both Stdin and Stdout are *os.File
	// terminals (handled internally by the docker provider via
	// `docker exec -it`).
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// TTY hints "the caller wants a terminal" even when the
	// streams aren't *os.File terminals (e.g. a parent process
	// piping through). Default false.
	TTY bool
}
