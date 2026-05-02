#!/usr/bin/env bash
#
# gh-sync-secrets.sh — push API keys from a .env file to this repo's
# GitHub Actions secrets so the live workflow (.github/workflows/live.yml)
# can run.
#
# Usage:
#   scripts/gh-sync-secrets.sh              # reads ./.env
#   scripts/gh-sync-secrets.sh ~/.env       # reads a different file
#   DRY_RUN=1 scripts/gh-sync-secrets.sh    # show what would be set
#
# Only env vars on the SECRETS allowlist below are uploaded — the
# rest of .env (HTML2MD_PROVIDER, TAVILY_TOPIC, MCP_AUTH_TOKEN, …)
# is left untouched. Empty / unset values are skipped silently so
# you can run this against a partial .env without having to scrub
# placeholders.
#
# Values are piped to `gh secret set` via stdin (omitting --body
# triggers stdin-read; `--body -` would set the literal string "-")
# instead of being passed on argv, so they never appear in `ps`
# output.
set -euo pipefail

ENV_FILE="${1:-.env}"
if [[ ! -f "$ENV_FILE" ]]; then
  echo "error: $ENV_FILE not found" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "error: gh CLI not on PATH (brew install gh)" >&2
  exit 1
fi

# Allowlist matches the env: block in .github/workflows/live.yml.
# Keep these in sync — adding a new provider env var means adding
# it here AND in the workflow.
SECRETS=(
  ANTHROPIC_API_KEY
  OPENAI_API_KEY
  PERPLEXITY_API_KEY
  XAI_API_KEY
  GOOGLE_API_KEY
  GOOGLE_CSE_ID
  TAVILY_API_KEY
  SERPAPI_KEY
  BRAVE_API_KEY
  X_API_KEY
  X_API_SECRET
  YOUTUBE_API_KEY
  BLUESKY_HANDLE
  BLUESKY_APP_PASSWORD
  JINA_API_KEY
)

# Read each value out of the .env file with a tiny parser. We don't
# `source` the file because that would execute arbitrary shell, and
# .env values can legitimately contain things like backticks. The
# parser handles three quoting styles seen in the wild:
#   KEY=value
#   KEY="value with spaces"
#   KEY='single-quoted'
read_env_value() {
  local key="$1"
  local line
  line=$(grep -E "^[[:space:]]*${key}=" "$ENV_FILE" | tail -n1 || true)
  [[ -z "$line" ]] && return 0
  # Strip leading whitespace and the KEY= prefix.
  local val="${line#*=}"
  # Drop surrounding double or single quotes — handle these BEFORE
  # the inline-comment strip below, because a `#` inside quotes is
  # literal data, not a comment.
  if [[ "$val" =~ ^\".*\"$ ]]; then
    val="${val#\"}"; val="${val%\"}"
  elif [[ "$val" =~ ^\'.*\'$ ]]; then
    val="${val#\'}"; val="${val%\'}"
  else
    # Unquoted value — strip inline comment ` #...` (whitespace
    # then #), matching Go's dotenv loader. Without this we'd ship
    # the comment text as part of the secret. Real-world hit:
    # `BLUESKY_HANDLE=jedi.be   # or your custom handle/domain`
    # was uploaded as the full string + comment, breaking Bluesky
    # auth in CI while the Go binary's loader handled it locally.
    val="${val%%[[:space:]]\#*}"
    # Trim trailing whitespace left after the strip (or already
    # present at the end of an unquoted value).
    val="${val%"${val##*[![:space:]]}"}"
  fi
  printf '%s' "$val"
}

set_count=0
skip_count=0
for key in "${SECRETS[@]}"; do
  val=$(read_env_value "$key")
  if [[ -z "$val" ]]; then
    echo "skip $key (empty / unset in $ENV_FILE)"
    skip_count=$((skip_count + 1))
    continue
  fi
  if [[ "${DRY_RUN:-}" == "1" ]]; then
    # Don't print the value — just confirm we would set it. Length
    # gives an at-a-glance sanity check (typo'd vs full key).
    echo "DRY: would set $key (${#val} chars)"
  else
    printf '%s' "$val" | gh secret set "$key"
    echo "set  $key"
  fi
  set_count=$((set_count + 1))
done

echo
echo "$set_count set, $skip_count skipped (out of ${#SECRETS[@]} known secrets)"
