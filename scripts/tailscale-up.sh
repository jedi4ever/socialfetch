#!/bin/bash
# tailscale-up.sh — shared userspace tailscale bring-up for the
# social-skills container family (agent, researcher, browser). Sourced
# (or invoked) by each image's entrypoint so a single TS_AUTHKEY in
# the operator's env puts every spawn on their tailnet without three
# copies of the recipe drifting.
#
# No-op when TS_AUTHKEY is unset — entrypoints can source this
# unconditionally; operators not using tailscale see zero overhead.
#
# Userspace mode (`tailscaled --tun=userspace-networking`) requires no
# NET_ADMIN cap, no /dev/net/tun mount — matches the docker-run flags
# the social-* binaries already pass.
#
# After bring-up, exports TS_SOCKET so subsequent `tailscale` CLI
# invocations Just Work without `--socket=/tmp/tailscaled.sock`.
#
# Hostname is "${TS_HOSTNAME_PREFIX:-tsi}-$(hostname)" so the operator
# can tell which container is which on the tailnet admin page. Each
# entrypoint sets TS_HOSTNAME_PREFIX appropriately before sourcing
# this (research / agent / browser).

if [ -n "${TS_AUTHKEY:-}" ]; then
    if ! command -v tailscaled >/dev/null 2>&1; then
        echo "tailscale-up: tailscaled not installed in image — skipping" >&2
    else
        tailscaled \
            --tun=userspace-networking \
            --socket=/tmp/tailscaled.sock \
            --state=/tmp/tailscaled.state \
            > /tmp/tailscaled.log 2>&1 &

        # Wait up to ~5s for the socket to appear.
        for _ in $(seq 1 10); do
            [ -S /tmp/tailscaled.sock ] && break
            sleep 0.5
        done

        # Hostname: <prefix>-<container-hostname>. The hostname inside
        # the container is docker's short ID by default — gives a
        # unique tailnet entry per spawn.
        TS_HOSTNAME="${TS_HOSTNAME_PREFIX:-tsi}-$(hostname)"

        # --reset clears any stale state from a previous run; the
        # auth key handles fresh registration. Ephemerality has to
        # come from the auth key being marked Ephemeral in the admin
        # UI — the older `tailscale up` CLI in debian:bookworm doesn't
        # accept --ephemeral.
        if ! tailscale --socket=/tmp/tailscaled.sock up \
            --authkey="$TS_AUTHKEY" \
            --hostname="$TS_HOSTNAME" \
            --reset; then
            echo "tailscale-up: tailscale up failed — continuing without tailnet" >&2
        fi

        # Make `tailscale status` etc Just Work in the rest of the
        # entrypoint + any user shell that follows.
        export TS_SOCKET=/tmp/tailscaled.sock
    fi
fi
