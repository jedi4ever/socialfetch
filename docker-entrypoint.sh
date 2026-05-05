#!/usr/bin/env sh
# Single-container supervisor for social-skills.
#
# Three known services, no auto-restart logic — if one dies we tear
# the others down and exit non-zero so Docker's restart policy
# (`restart: unless-stopped` in compose, `--restart unless-stopped`
# on `docker run`) brings the whole container back. That's
# intentionally simpler than s6-overlay / supervisord: three known
# services + a process manager outside the container = same
# observable behaviour as a more elaborate supervisor.
#
# Invocation modes:
#
#   all       (default) — start headless + ledger + mcp, wait on
#              any to exit, propagate SIGTERM.
#   headless  — exec social-fetch headless run on :5556 alone.
#   ledger    — exec social-ledger daemon run on :5557 alone.
#   mcp       — exec social-fetch mcp --http :5558 alone.
#   shell     — drop into /bin/sh for debugging (no daemons).
#   <other>   — exec verbatim (passthrough so `docker run image
#              social-fetch fetch URL` works).

set -eu

mode="${1:-all}"
shift || true

# Respect operator-supplied bind addresses via env without forcing
# them to remember the flags. CLI flags override env if the operator
# passes extra args (handled by the binaries themselves).
HEADLESS_BIND="${HEADLESS_BIND:-0.0.0.0:5556}"
LEDGER_BIND="${LEDGER_BIND:-0.0.0.0:5557}"
MCP_BIND="${MCP_BIND:-:5558}"

case "$mode" in
  headless)
    exec social-fetch headless run --bind "$HEADLESS_BIND" "$@"
    ;;
  ledger)
    exec social-ledger daemon run --bind "$LEDGER_BIND" "$@"
    ;;
  mcp)
    exec social-fetch mcp --http "$MCP_BIND" "$@"
    ;;
  shell)
    exec /bin/sh
    ;;
  all)
    # Start in dependency order: headless first (slow startup, ~2s
    # per browser warmup), then ledger, then mcp which depends on
    # both being reachable.
    social-fetch headless run --bind "$HEADLESS_BIND" &
    HEADLESS_PID=$!

    social-ledger daemon run --bind "$LEDGER_BIND" &
    LEDGER_PID=$!

    # Tiny stagger so the MCP server's first health probe of
    # headless / ledger lands after their listeners are bound.
    sleep 1

    social-fetch mcp --http "$MCP_BIND" &
    MCP_PID=$!

    # Forward SIGTERM / SIGINT to children. dumb-init reaps PID 1
    # signals; we propagate to the actual workers so they get a
    # chance to flush state (ledger SQLite checkpoint, headless
    # browser cleanup).
    trap 'kill -TERM $HEADLESS_PID $LEDGER_PID $MCP_PID 2>/dev/null || true; wait' TERM INT

    # Block until any one exits. `wait -n` was POSIX-added in
    # bash 4.3 / busybox sh — the debian:bookworm-slim image has
    # `dash` as /bin/sh, which doesn't support -n. Fall back to a
    # poll loop.
    while kill -0 $HEADLESS_PID 2>/dev/null \
       && kill -0 $LEDGER_PID   2>/dev/null \
       && kill -0 $MCP_PID      2>/dev/null; do
      sleep 2
    done

    # One of them died — log which one and tear the rest down.
    if ! kill -0 $HEADLESS_PID 2>/dev/null; then
      echo "social-skills: headless daemon exited" >&2
    fi
    if ! kill -0 $LEDGER_PID 2>/dev/null; then
      echo "social-skills: ledger daemon exited" >&2
    fi
    if ! kill -0 $MCP_PID 2>/dev/null; then
      echo "social-skills: mcp server exited" >&2
    fi

    kill -TERM $HEADLESS_PID $LEDGER_PID $MCP_PID 2>/dev/null || true
    wait || true
    exit 1
    ;;
  *)
    # Passthrough: `docker run image social-fetch fetch <url>` etc.
    exec "$mode" "$@"
    ;;
esac
