#!/bin/bash
# claude-creds-decode.sh — when CLAUDE_OAUTH_CREDENTIALS is set in
# the container env, decode the base64 blob into the file claude-code
# reads on startup (~/.claude/.credentials.json). No-op when the var
# isn't set.
#
# Lives in scripts/ + sourced by both docker-agent-entrypoint.sh
# and scripts/researcher-entrypoint.sh — same DRY pattern as
# tailscale-up.sh + claude-onboarding.sh. Without this, the
# researcher path silently dropped the OAuth blob even though the
# host forwarded it correctly: claude inside the container would
# come up "not logged in" because the file simply didn't exist.
#
# Strip whitespace before decoding — copy-paste from a terminal
# tends to wrap base64 with newlines that GNU base64 accepts but
# busybox/alpine don't; tr is portable.

if [ -n "${CLAUDE_OAUTH_CREDENTIALS:-}" ]; then
  mkdir -p "$HOME/.claude"
  printf '%s' "$CLAUDE_OAUTH_CREDENTIALS" | tr -d '[:space:]' | base64 -d > "$HOME/.claude/.credentials.json"
  chmod 0600 "$HOME/.claude/.credentials.json"
fi
