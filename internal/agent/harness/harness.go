// Package harness abstracts "which coding-agent CLI runs inside the
// session container." Today only claude-code; tomorrow codex /
// gemini / copilot / cursor / tessl, each as a separate file under
// this package. Mirrors dclaude's src/extensions/<name>/ shape but
// flatter — no per-harness install scripts because the docker image
// already bakes the agent in at build time.
//
// Harness owns three pieces of harness-specific knowledge:
//
//   - The shape of an "invoke prompt" command line (claude-code's
//     `claude --print --dangerously-skip-permissions <prompt>` vs
//     codex's flag set, etc.).
//   - The shape of an "interactive" command line (PTY shell into
//     the agent — `claude` for claude-code).
//   - How to extract auth from the host environment (env var
//     passthrough; later, OAuth-from-Keychain).
package harness

import (
	"fmt"
	"sort"
	"strings"
)

// Harness is the per-CLI interface. Stateless — implementations
// are typically empty structs. Get(name) returns the registered
// instance.
type Harness interface {
	// Name is the lowercase identifier (e.g. "claude-code"). Used
	// as the value for UpOpts.Harness and for `social-agent harness
	// list` output.
	Name() string

	// InvokePrompt returns argv to run a one-shot prompt. The
	// container's entrypoint passes this through to exec(2) when
	// the docker CMD is ["run", "<prompt>"]. Today the entrypoint
	// hardcodes claude-code's invocation; this method is the
	// extension hook for when we add a second harness.
	InvokePrompt(prompt string) []string

	// InteractiveCmd returns argv for an interactive PTY session
	// (no prompt, the user is going to drive the agent live).
	// `social-agent exec <id>` with no command runs this.
	InteractiveCmd() []string

	// ResumeCmd returns argv for a "continue the previous
	// conversation in this session" PTY entry. claude-code uses
	// `claude --continue` to resume the most recent stored chat;
	// harnesses without a conversation concept (echo) fall back
	// to InteractiveCmd. `social-agent session resume <id>` runs
	// this — the difference from InteractiveCmd is that the
	// agent's stateful conversation history is loaded.
	ResumeCmd() []string

	// EnvFromHost reads operator-side env vars and returns the set
	// to inject into the container. Today: pass through
	// ANTHROPIC_API_KEY / CLAUDE_OAUTH_CREDENTIALS for claude-code.
	// Future: extract OAuth creds from macOS Keychain when neither
	// env is set.
	EnvFromHost(host map[string]string) (map[string]string, error)
}

// StreamingJSONHarness is an optional capability harnesses can
// implement when their CLI supports JSONL stdin/stdout. claude-code
// has it via `--input-format=stream-json --output-format=stream-json`.
// When implemented, the docker provider's runStream prefers this
// path over InvokePrompt — the upside is richer output events
// (assistant turns, tool_use, tool_result, result with usage and
// stop_reason) and a stdin that stays open across turns, which is
// what the v0.18.1 multi-turn `social_agent_send` will plug into.
//
// Today (v0.18.0) we only use it for one-shot runs: write one
// user-message JSONL to stdin, close stdin, drain output until the
// process exits. Multi-turn comes in the follow-up.
type StreamingJSONHarness interface {
	// StreamJSONCmd returns argv to run the harness in JSONL
	// stream-mode. NO prompt argument — the prompt arrives over
	// stdin as a WrapUserMessage line. The container's entrypoint
	// passes this through to exec(2) when the docker CMD is the
	// streaming runner.
	StreamJSONCmd() []string

	// WrapUserMessage encodes one user-turn as a JSONL line
	// (trailing newline included). The returned bytes are written
	// verbatim to the harness's stdin. Multiple turns can be
	// concatenated by writing multiple WrapUserMessage lines
	// before closing stdin; for v0.18.0 we send exactly one.
	WrapUserMessage(text string) []byte
}

// registry is the in-process catalog of known harnesses, populated
// at init time from each harness's own file.
var registry = map[string]Harness{}

// Register adds a harness implementation under its Name(). Called
// from each harness file's init(); panics on duplicate name to
// surface a programming error at startup rather than silently
// shadowing.
func Register(h Harness) {
	if _, ok := registry[h.Name()]; ok {
		panic(fmt.Sprintf("harness %q already registered", h.Name()))
	}
	registry[h.Name()] = h
}

// Get returns the harness registered under name, or an error
// listing what's available. Used by Provider implementations and
// by `social-agent harness list`.
func Get(name string) (Harness, error) {
	if name == "" {
		name = "claude-code"
	}
	h, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown harness %q (try: %s)", name, strings.Join(Names(), " | "))
	}
	return h, nil
}

// Names returns every registered harness, sorted by name. Used by
// `social-agent harness list` and by Get's error message.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
