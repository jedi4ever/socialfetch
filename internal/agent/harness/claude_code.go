package harness

// claude_code.go is the first Harness implementation: Anthropic's
// Claude Code (`claude` CLI). Extends to other harnesses (codex,
// gemini, …) by adding parallel files; the agent provider stays
// harness-agnostic.

import (
	"encoding/json"
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
written them to /artifacts.

You also have a ` + "`social`" + ` MCP server backed by a shared content ledger.
BEFORE fetching any URL, call ` + "`social_ledger_seen`" + ` — if seen=true, call
` + "`social_ledger_get`" + ` for the cached body instead of re-fetching. Use
` + "`social_fetch_fetch`" + ` for misses; that fetch auto-records into the ledger
so the next run hits the cache. Prefer ` + "`social_fetch_fetch`" + ` over Claude
Code's built-in WebFetch so the result lands in the ledger.

You also have an ` + "`ask_user`" + ` tool from the ` + "`ask`" + ` MCP server. It forwards a
plain-English question to the human operator and returns their reply.
Use it when you need information that is not in your context — credentials,
file locations, business decisions, scope clarifications. Don't use it for
trivial things; the operator's attention is expensive. If the tool errors
"not available", you're running outside an MCP session — don't retry, just
proceed with your best guess and surface the assumption in your answer.`

// innerMCPConfigJSON is the inline --mcp-config payload that
// registers the in-container MCP servers with claude-code:
//
//   - ask    → social-agent ask-mcp serve. Forwards ask_user
//     questions to the outer Claude Code session via
//     SOCIAL_AGENT_CALLBACK_URL. Handler errors cleanly
//     when the env var is unset (CLI runs).
//   - social → social-fetch mcp. Exposes the full social-fetch
//     tool surface (fetch, search, ask, ledger_*, …).
//     Read-side ledger tools auto-route to the host
//     daemon when SOCIAL_LEDGER_DAEMON_URL is set.
//
// Built once at package init via json.Marshal so the JSON is
// always well-formed. Both binaries live at /usr/local/bin/ in
// the agent image (see Dockerfile.agent's COPY layer).
var innerMCPConfigJSON = buildInnerMCPConfigJSON()

func buildInnerMCPConfigJSON() string {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"ask": map[string]any{
				"command": "/usr/local/bin/social-agent",
				"args":    []string{"ask-mcp", "serve"},
			},
			"social": map[string]any{
				"command": "/usr/local/bin/social-fetch",
				"args":    []string{"mcp"},
			},
		},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		// Map of strings only — json.Marshal can't fail here.
		// Panic is fine because this runs at package init.
		panic("buildInnerMCPConfigJSON: " + err.Error())
	}
	return string(body)
}

// InvokePrompt returns the argv for a one-shot prompt. The flags
// match what `claude --help` documents:
//
//	--print                          headless mode (write answer
//	                                 to stdout, exit when done)
//	--dangerously-skip-permissions   skip the per-tool prompt — the
//	                                 container is the sandbox, the
//	                                 whole point is to give claude
//	                                 full freedom inside.
//	--mcp-config <json>              register the in-container MCP
//	                                 servers (ask + social) so the
//	                                 inner agent can elicit the
//	                                 outer operator and consult the
//	                                 shared content ledger.
//	--append-system-prompt <text>    inject the /artifacts convention
//	                                 so claude writes returnable
//	                                 files to the right place.
func (ClaudeCode) InvokePrompt(prompt string) []string {
	return []string{
		"claude",
		"--print",
		"--dangerously-skip-permissions",
		"--mcp-config", innerMCPConfigJSON,
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

// StreamJSONCmd is the stream-json variant of InvokePrompt. The
// prompt comes over stdin as a WrapUserMessage line; argv carries
// only the flags. `--verbose` is required when both
// --input-format and --output-format are stream-json — without it
// claude refuses to start.
//
// --mcp-config registers the in-container ask + social MCP
// servers so the inner agent can elicit the outer operator and
// consult the shared content ledger. Always included; ask_user
// returns a clean error when no SOCIAL_AGENT_CALLBACK_URL is set
// (CLI runs without an MCP outer), and the ledger tools degrade
// gracefully when no SOCIAL_LEDGER_DAEMON_URL is set.
func (ClaudeCode) StreamJSONCmd() []string {
	return []string{
		"claude",
		"--print",
		"--input-format=stream-json",
		"--output-format=stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--mcp-config", innerMCPConfigJSON,
		"--append-system-prompt", artifactsSystemPrompt,
	}
}

// WrapUserMessage encodes one user turn as the JSONL envelope
// claude-code expects on stdin in stream-json mode:
//
//	{"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}
//
// Trailing newline included so callers can write the bytes
// verbatim and concatenate multiple turns. json.Marshal panics
// only on un-encodable types — we feed it strings, so the error
// is unreachable in practice.
func (ClaudeCode) WrapUserMessage(text string) []byte {
	body, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	})
	return append(body, '\n')
}

func init() {
	Register(ClaudeCode{})
}
