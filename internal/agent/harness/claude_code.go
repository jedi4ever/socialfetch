package harness

// claude_code.go is the first Harness implementation: Anthropic's
// Claude Code (`claude` CLI). Extends to other harnesses (codex,
// gemini, …) by adding parallel files; the agent provider stays
// harness-agnostic.

import (
	"strings"
)

// ClaudeCode is a stateless harness — no fields, no construction.
// Registered at init via the package-level Register call.
type ClaudeCode struct{}

func (ClaudeCode) Name() string { return "claude-code" }

// artifactsSystemPrompt is appended to claude's system prompt
// before every InvokePrompt run. Tells claude about the
// /artifacts outbox convention so it writes returnable files
// there instead of the work-cwd /workspace (which doesn't come
// back over the wire on substrates without bind-mounts).
//
// Kept short on purpose — claude is good at following terse
// system instructions, and a long preamble eats context budget
// the user's actual prompt should be using.
const artifactsSystemPrompt = `You are running in a sandboxed container.

Two conventions about the filesystem:

  /workspace   your cwd. May be empty (no host bind-mount) or pre-populated
               with the operator's repo. Edits here DO NOT come back to the
               operator unless the operator explicitly mounted it; treat as
               scratch unless told otherwise.

  /artifacts   your outbox. Files you write here are pulled back to the
               operator after this run. Use it for any file the operator
               asked you to produce — rendered markdown, screenshots,
               PNG/JPG images, code files, JSON, etc. Write binary outputs
               directly to /artifacts/<name>; do NOT base64-encode them in
               your text response.

Your text response (this conversation) is also returned separately on
stdout. Use it for explanation, summary, and references to artifacts you
produced. Don't dump file contents in the response when you've already
written them to /artifacts.`

// InvokePrompt returns the argv for a one-shot prompt. The flags
// match what `claude --help` documents:
//
//	--print                          headless mode (write answer
//	                                 to stdout, exit when done)
//	--dangerously-skip-permissions   skip the per-tool prompt — the
//	                                 container is the sandbox, the
//	                                 whole point is to give claude
//	                                 full freedom inside.
//	--append-system-prompt <text>    inject the /artifacts convention
//	                                 so claude writes returnable
//	                                 files to the right place.
func (ClaudeCode) InvokePrompt(prompt string) []string {
	return []string{
		"claude",
		"--print",
		"--dangerously-skip-permissions",
		"--append-system-prompt", artifactsSystemPrompt,
		prompt,
	}
}

// InteractiveCmd is the bare `claude` command — drops the operator
// into Claude Code's interactive UI with a fresh chat. Useful when
// the operator `social-agent exec`s into a session and wants to
// start a new conversation.
func (ClaudeCode) InteractiveCmd() []string {
	return []string{"claude"}
}

// ResumeCmd is `claude --continue`, which loads the session
// container's most-recent conversation from ~/.claude/conversations/
// and continues it. Different from InteractiveCmd: that one
// always starts fresh. Operators iterate on multi-turn work via
// `social-agent session resume <id>` between prompt-runs.
func (ClaudeCode) ResumeCmd() []string {
	return []string{"claude", "--continue"}
}

// EnvFromHost selects the auth-related env vars to forward. Today:
//
//   - ANTHROPIC_API_KEY        — direct API key, simplest path
//   - CLAUDE_OAUTH_CREDENTIALS — base64 of the OAuth credentials
//     JSON (operator pre-extracts via
//     dclaude's credentials.sh today;
//     v0.17 will do this automatically
//     from the macOS Keychain).
//
// Either suffices on its own; the entrypoint's auth-precedence
// logic prefers OAuth credentials when both are set. Returning an
// empty map is fine — claude --print will surface its own auth
// error and we don't try to be smarter than upstream.
//
// host is the operator's full env map (typically os.Environ()
// parsed); we read only the keys we recognise so unrelated env
// pollution doesn't leak into the container.
func (ClaudeCode) EnvFromHost(host map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, key := range []string{
		"ANTHROPIC_API_KEY",
		"CLAUDE_OAUTH_CREDENTIALS",
	} {
		if v, ok := host[key]; ok && strings.TrimSpace(v) != "" {
			out[key] = v
		}
	}
	return out, nil
}

func init() {
	Register(ClaudeCode{})
}
