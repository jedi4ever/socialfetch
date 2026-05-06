#!/usr/bin/env bash
# Container-side supervisor for social-skills-agent. Decodes Claude
# Code credentials (when injected from the host as base64 via
# CLAUDE_OAUTH_CREDENTIALS), then dispatches on $1 to one of:
#
#   sleep                   keep the container alive (default; lets
#                           `social-agent up` then `... exec` work).
#   run "<prompt>"          claude --print "<prompt>" (one-shot;
#                           writes the response to stdout, exits when
#                           claude does).
#   shell                   /bin/bash -l (interactive PTY; only useful
#                           when the docker run was -it).
#   exec <cmd...>           exec "$@" (any command, e.g.
#                           `social-fetch fetch <url>`).
#
# Auth precedence (matches dclaude's extensions/claude/setup.sh):
#   - CLAUDE_OAUTH_CREDENTIALS (base64 of OAuth blob from host) wins
#     if present — decoded into ~/.claude/.credentials.json.
#   - Otherwise ANTHROPIC_API_KEY (env passthrough) is what claude
#     picks up at startup. If neither is set, claude --print errors
#     out with its own auth message — we don't try to be smarter
#     than the upstream.
#
# Env passthrough we explicitly care about:
#   SOCIAL_FETCH_HEADLESS_DAEMON_URL  — points the in-container
#                                       social-fetch at the operator's
#                                       chromedp pool. host.docker.internal
#                                       is the magic name docker-on-mac
#                                       resolves to the host.
#   SOCIAL_LEDGER_DAEMON_URL          — same, for the ledger daemon.
#   ANTHROPIC_API_KEY / CLAUDE_OAUTH_CREDENTIALS — auth.

set -eu

# ----- claude onboarding pre-population -----
# Mirrors ~/dev/dclaude/src/extensions/claude/setup.sh. claude --print
# (the agent path) silently skips the first-run wizard, but the
# interactive TUI shown by `claude` (the social-researcher path) walks
# the operator through onboarding + workspace-trust + API-key approval
# every fresh container, which is annoying when we own the sandbox.
# Pre-populate the marker files so claude considers itself already
# onboarded; idempotent — bail if either file already exists so a
# host-mounted ~/.claude wins.
CLAUDE_JSON="$HOME/.claude.json"
CLAUDE_DIR="$HOME/.claude"
CLAUDE_INTERNAL_JSON="$CLAUDE_DIR/claude.json"
if [ ! -e "$CLAUDE_JSON" ] && [ ! -d "$CLAUDE_DIR" ]; then
  mkdir -p "$CLAUDE_DIR"
  cat > "$CLAUDE_JSON" <<'EOF'
{
  "hasCompletedOnboarding": true,
  "hasTrustDialogAccepted": true,
  "bypassPermissionsModeAccepted": true,
  "projects": {
    "/workspace": {
      "allowedTools": [],
      "hasTrustDialogAccepted": true,
      "hasCompletedProjectOnboarding": true
    }
  }
}
EOF
  cat > "$CLAUDE_INTERNAL_JSON" <<'EOF'
{
  "hasTrustDialogHooksAccepted": true,
  "hasCompletedOnboarding": true
}
EOF
  # Pre-approve the supplied API key so claude doesn't show the
  # "approve this API key" prompt on first run. The marker is the
  # last 20 chars (matches what claude stores). Skip silently when
  # ANTHROPIC_API_KEY isn't set — OAuth path or no-auth flows.
  if [ -n "${ANTHROPIC_API_KEY:-}" ] && command -v jq >/dev/null 2>&1; then
    KEY_TAIL="${ANTHROPIC_API_KEY: -20}"
    tmp="$(mktemp)"
    jq --arg ak "$KEY_TAIL" '.customApiKeyResponses = {approved:[$ak], rejected:[]}' \
      "$CLAUDE_JSON" > "$tmp" && mv "$tmp" "$CLAUDE_JSON"
  fi
fi

# ----- credentials -----
if [ -n "${CLAUDE_OAUTH_CREDENTIALS:-}" ]; then
  mkdir -p "$HOME/.claude"
  # Strip whitespace before decoding — copy-paste from a terminal
  # tends to wrap base64 with newlines that base64(1) on debian
  # accepts but on alpine doesn't; tr is portable.
  printf '%s' "$CLAUDE_OAUTH_CREDENTIALS" | tr -d '[:space:]' | base64 -d > "$HOME/.claude/.credentials.json"
  chmod 0600 "$HOME/.claude/.credentials.json"
fi

# ----- artifacts server -----
# Start the artifacts HTTP server in the background. Stays up for
# the container's lifetime so the operator can pull mid-prompt
# (long-running sessions) or post-run (one-shot). Output to a log
# file so it doesn't muddy claude's stdout in `run` mode.
mkdir -p /artifacts
nohup social-agent artifacts serve --root /artifacts --bind 0.0.0.0:5563 \
    > /tmp/artifacts-server.log 2>&1 < /dev/null &
disown

# ----- mode dispatch -----
mode="${1:-sleep}"
shift || true

case "$mode" in
  sleep)
    # Keep the container alive so `social-agent exec` can enter it.
    # tail -f /dev/null is the canonical PID-1-reaper-friendly idle
    # — `sleep infinity` works too but tail's signal handling is
    # cleaner under dumb-init.
    exec tail -f /dev/null
    ;;
  run)
    # One-shot: pass everything after `run` to claude --print as a
    # single prompt. The shell already concatenated the args; we
    # just `$*` them. --dangerously-skip-permissions because the
    # container is the sandbox — the whole point is to give claude
    # full access inside without prompting.
    if [ "$#" -lt 1 ]; then
      echo "social-agent: run requires a prompt" >&2
      exit 2
    fi
    exec claude --print --dangerously-skip-permissions "$*"
    ;;
  shell)
    exec /bin/bash -l
    ;;
  exec)
    if [ "$#" -lt 1 ]; then
      echo "social-agent: exec requires a command" >&2
      exit 2
    fi
    exec "$@"
    ;;
  *)
    # Unknown first arg — treat the whole argv as the command to
    # exec. Lets `docker run social-skills-agent which claude`
    # behave the same as bare exec.
    exec "$mode" "$@"
    ;;
esac
