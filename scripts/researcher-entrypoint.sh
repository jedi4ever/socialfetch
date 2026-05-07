#!/bin/bash
# researcher-entrypoint.sh — runs first inside a social-researcher
# container. Brings tailscale up via the shared tailscale-up.sh helper
# (no-op when TS_AUTHKEY isn't set), then exec's whatever command
# `docker run` passed in (bash by default, claude in --claude mode).
#
# All the bring-up machinery lives in tailscale-up.sh so the same
# logic stays in lockstep across the agent / browser / researcher
# entrypoints. Set TS_HOSTNAME_PREFIX so this container's tailnet
# entry is identifiable as a research session.

set -e

export TS_HOSTNAME_PREFIX="${TS_HOSTNAME_PREFIX:-research}"
. /usr/local/bin/tailscale-up.sh

# Pre-populate claude's first-run markers so the TUI lands directly
# in a usable session (same pre-pop the agent entrypoint does).
if [ -x /usr/local/bin/claude-onboarding.sh ]; then
  . /usr/local/bin/claude-onboarding.sh
fi

exec "$@"
