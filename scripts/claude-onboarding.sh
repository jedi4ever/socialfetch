#!/bin/bash
# claude-onboarding.sh — pre-populate claude-code's first-run markers
# so the interactive TUI doesn't walk through onboarding + workspace-
# trust + API-key approval on every fresh container.
#
# The agent path (`claude --print`) silently skips the wizard, but the
# researcher's `claude` TUI hits it on a clean $HOME. We write the
# minimal "already onboarded" markers up-front; idempotent — skipped
# entirely when either file already exists, so a host-mounted
# ~/.claude wins.
#
# Lives in scripts/ + sourced by both docker-agent-entrypoint.sh and
# scripts/researcher-entrypoint.sh, matching the tailscale-up.sh
# pattern. Single source of truth keeps the two entrypoints from
# drifting on the marker schema.

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
