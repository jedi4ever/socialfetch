package agent

// envpass.go — operator → container env passthrough. The
// social-fetch / social-ledger / social-browser binaries inside
// an agent container all read configuration from a known set of
// env vars (provider API keys, daemon URLs, etc.). Operators
// expect "if I set BRAVE_API_KEY in my .env, claude inside the
// agent should be able to use brave search." This helper
// implements that — both providers (docker, daytona) call it
// to compose the final container env from the operator's host
// env.
//
// Single source of truth for "which env vars get passed?". When
// a new provider lands in social-fetch (and gains a *_API_KEY),
// add it here AND to extensions/claude-desktop/manifest.json's
// user_config — the two lists should stay parallel.

import (
	"strings"
)

// PassthroughKeys is the canonical list of host env vars that
// flow into agent containers. Mirrors
// extensions/claude-desktop/manifest.json's user_config keys —
// any env var an operator might set on their host that
// social-fetch / social-ledger / social-browser would read.
//
// Added explicitly rather than via a regex match so a stray
// $RANDOM env var or a personal $TOKEN doesn't accidentally
// leak into the container. The list is verbose; that's the
// point — surface what's passing through.
var PassthroughKeys = []string{
	// Anthropic / claude-code
	"ANTHROPIC_API_KEY",
	"CLAUDE_OAUTH_CREDENTIALS",

	// LLM ask providers
	"OPENAI_API_KEY",
	"PERPLEXITY_API_KEY",
	"XAI_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
	"GOOGLE_CSE_ID",

	// Search / fetch providers
	"TAVILY_API_KEY",
	"TAVILY_TOPIC",
	"SERPAPI_KEY",
	"BRAVE_API_KEY",
	"X_API_KEY",
	"X_API_SECRET",
	"YOUTUBE_API_KEY",
	"GITHUB_TOKEN",
	"BLUESKY_HANDLE",
	"BLUESKY_APP_PASSWORD",
	"JINA_API_KEY",

	// social-fetch / social-ledger / social-browser knobs
	"HTML2MD_PROVIDER",
	"HTML2MD_READER",
	"SOCIAL_FETCH_HEADLESS_DAEMON_URL",
	"SOCIAL_FETCH_HEADLESS_DAEMON_TOKEN",
	"SOCIAL_FETCH_HEADLESS_USER_AGENT",
	"SOCIAL_FETCH_HEADLESS_TIMEOUT",
	"SOCIAL_FETCH_HEADLESS_SETTLE",
	"SOCIAL_FETCH_AUDIT",
	"SOCIAL_LEDGER_DIR",
	"SOCIAL_LEDGER_READONLY",
	"SOCIAL_LEDGER_DAEMON_URL",
	"SOCIAL_LEDGER_DAEMON_TOKEN",
	"SOCIAL_BRIDGE_URL",

	// MCP HTTP transport — `social-{agent,ledger,fetch} mcp --http`
	// gates `/mcp` on a bearer token from MCP_AUTH_TOKEN. Forwarded
	// so the inner claude (in a social-researcher container, agent
	// container, etc) presents the same Authorization header when it
	// dials those host servers. Same value on both sides; no rotation.
	"MCP_AUTH_TOKEN",

	// Slack notifier — social-notifier's slack provider posts via
	// chat.postMessage with a Bot Token (xoxb-…). Forwarded so an
	// agent / researcher container can `social-notifier post …`
	// without the operator having to set the token on every run.
	// Default channel is optional; --channel on the call wins.
	"SLACK_BOT_TOKEN",
	"SLACK_DEFAULT_CHANNEL",

	// Tailscale — when set, the social-researcher container can
	// `tailscale up --authkey=$TS_AUTHKEY` and reach the operator's
	// tailnet (alternative to host.docker.internal for cross-host
	// setups). Pre-auth keys from
	// https://login.tailscale.com/admin/settings/keys; ephemeral +
	// short-lived recommended.
	"TS_AUTHKEY",

	// HOST_TAILSCALE_NAME — operator's laptop hostname on the
	// tailnet (e.g. "mac" or "mac.your-tailnet.ts.net"). When set,
	// the social-researcher binary uses it as the host portion of
	// the default --agent-mcp-url / --ledger-mcp-url so the inner
	// claude reaches the host MCP servers via tailnet DNS instead
	// of host.docker.internal. Operator sets it once in .env;
	// PassthroughKeys forwards to every container.
	"HOST_TAILSCALE_NAME",

	// Observability — when set the in-container social-fetch
	// emits OTel traces; useful for debugging agent runs.
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_HEADERS",
	"OTEL_SERVICE_NAME",
}

// BuildPassthroughEnv reads PassthroughKeys from the supplied
// host env map (typically `parseEnviron(os.Environ())`) and
// returns the subset that's set + non-empty. Callers merge
// operator-supplied UpOpts.Env over this so explicit `--env
// KEY=VAL` always wins.
//
// Empty values are dropped. Operators sometimes set an env var
// to "" to "unset" it locally; we don't want that to override
// the in-container default.
func BuildPassthroughEnv(host map[string]string) map[string]string {
	out := make(map[string]string, len(PassthroughKeys))
	for _, key := range PassthroughKeys {
		if v, ok := host[key]; ok && strings.TrimSpace(v) != "" {
			out[key] = v
		}
	}
	return out
}

// RewriteLoopbackURL swaps host-loopback names (127.0.0.1,
// 0.0.0.0, localhost) inside `http://`-shape URLs with
// `host.docker.internal`. The container's own loopback is its
// own; reaching the host's loopback requires the magic
// host.docker.internal name (paired with --add-host on the docker
// run argv). Lives here rather than in providers/docker because
// every host-side caller composing a container env wants the same
// rewrite — docker provider, social-researcher, anything else.
//
// Non-URL values pass through unchanged so plain string env vars
// (FOO=bar) aren't mangled by an over-eager regex.
func RewriteLoopbackURL(v string) string {
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
