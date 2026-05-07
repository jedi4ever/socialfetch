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
# NET_ADMIN cap, no /dev/net/tun mount. As a tradeoff the kernel
# network stack inside the container does NOT route to the tailnet —
# we expose a SOCKS5 + HTTP proxy on localhost so curl, wget, claude,
# etc. can reach tailnet hosts via HTTPS_PROXY/HTTP_PROXY/ALL_PROXY.
#
# Socket path: tries to use the tailscale CLI default
# (/var/run/tailscale/tailscaled.sock) when sudo is available so
# `tailscale status` etc Just Work without --socket. Falls back to
# /tmp/tailscaled.sock when sudo isn't available — operator must
# pass --socket or use $TS_SOCKET.
#
# Hostname is "${TS_HOSTNAME_PREFIX:-tsi}-$(hostname)" so the operator
# can tell which container is which on the tailnet admin page. Each
# entrypoint sets TS_HOSTNAME_PREFIX appropriately before sourcing
# this (research / agent / browser).

if [ -n "${TS_AUTHKEY:-}" ]; then
    if ! command -v tailscaled >/dev/null 2>&1; then
        echo "tailscale-up: tailscaled not installed in image — skipping" >&2
    else
        # Prefer the default socket location so tailscale CLI works
        # without --socket. /var/run is tmpfs and not writable by the
        # non-root user, but sudo NOPASSWD (agent + researcher images)
        # lets us mkdir + chown it once at boot. Browser image has no
        # sudoers entry → fall back to /tmp.
        TS_SOCK=/var/run/tailscale/tailscaled.sock
        if [ ! -d /var/run/tailscale ]; then
            if command -v sudo >/dev/null 2>&1 \
               && sudo -n mkdir -p /var/run/tailscale 2>/dev/null \
               && sudo -n chown "$(id -u):$(id -g)" /var/run/tailscale 2>/dev/null; then
                : # default path now writable
            else
                TS_SOCK=/tmp/tailscaled.sock
            fi
        fi

        # Userspace daemon + outbound proxies for curl/claude/etc.
        # Flag name is --outbound-http-proxy-LISTEN (not -server) —
        # tailscaled --help is the source of truth; running with the
        # wrong name prints help and exits without starting.
        tailscaled \
            --tun=userspace-networking \
            --socket="$TS_SOCK" \
            --state=/tmp/tailscaled.state \
            --socks5-server=localhost:1055 \
            --outbound-http-proxy-listen=localhost:1056 \
            > /tmp/tailscaled.log 2>&1 &

        # Wait up to ~5s for the socket to appear.
        for _ in $(seq 1 10); do
            [ -S "$TS_SOCK" ] && break
            sleep 0.5
        done

        # --reset clears stale state from a previous run; auth key
        # handles fresh registration. Ephemerality comes from the auth
        # key being marked Ephemeral in the admin UI (older bookworm
        # CLIs don't accept --ephemeral).
        TS_HOSTNAME="${TS_HOSTNAME_PREFIX:-tsi}-$(hostname)"
        if ! tailscale --socket="$TS_SOCK" up \
            --authkey="$TS_AUTHKEY" \
            --hostname="$TS_HOSTNAME" \
            --reset; then
            echo "tailscale-up: tailscale up failed — continuing without tailnet" >&2
        fi

        # Outbound traffic to tailnet hosts (HTTP, HTTPS, anything
        # SOCKS-aware) routes through tailscaled's userspace proxy.
        # Loopback + docker bridge stay direct via NO_PROXY so
        # localhost calls and host.docker.internal Just Work.
        export HTTP_PROXY="http://localhost:1056"
        export HTTPS_PROXY="http://localhost:1056"
        export ALL_PROXY="socks5://localhost:1055"
        export NO_PROXY="localhost,127.0.0.1,::1,host.docker.internal,.docker.internal,.local"
        # Lowercase forms — wget + a handful of older tools only read these.
        export http_proxy="$HTTP_PROXY"
        export https_proxy="$HTTPS_PROXY"
        export all_proxy="$ALL_PROXY"
        export no_proxy="$NO_PROXY"

        # If we're using the non-default socket path, expose it as
        # TS_SOCKET so any wrapper scripts that consult it find the
        # daemon without having to type --socket. Note: the tailscale
        # CLI itself does NOT honor TS_SOCKET; it's purely informational.
        if [ "$TS_SOCK" != "/var/run/tailscale/tailscaled.sock" ]; then
            export TS_SOCKET="$TS_SOCK"
        fi
    fi
fi
