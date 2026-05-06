# syntax=docker/dockerfile:1.7
#
# social-skills runtime container — single stage. The Go binaries are
# cross-compiled on the host (see Makefile linux-binaries-* targets)
# and copied in via TARGETARCH. Switching between arm64 (apple-silicon
# local dev) and amd64 (Daytona push) reuses every layer below the
# COPYs — apt install of chromium + fonts is the slow step and it's
# arch-portable. Result: re-arch image build takes seconds, not
# minutes.
#
# Pre-v0.15.4 had a multi-stage Dockerfile with a `golang:1.26-bookworm
# AS builder` stage that ran `go build` inside docker. That re-built
# Go from scratch on every arch flip because the layer cache for
# `RUN go build` is per-platform. The host-cross-compile model here
# fixes that and also makes Go errors visible in `make` output instead
# of buried in `docker build` logs.

FROM debian:bookworm-slim

# TARGETARCH is set by docker buildx --platform; selects which
# dist/linux-<arch>/ tree to copy the binaries from.
ARG TARGETARCH

# chromium — what chromedp drives via CDP. We don't install
# chromium-driver because we use CDP, not WebDriver.
# fonts-liberation + fonts-noto-cjk — patai's tests show missing
# glyphs trigger anti-bot detection on Asian-language sites.
# ca-certificates — chromium needs a working trust store for HTTPS.
# dumb-init — PID 1 reaper for clean SIGTERM propagation under
# `docker stop`.
# curl — the HEALTHCHECK + ad-hoc debugging probe.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      chromium \
      fonts-liberation \
      fonts-noto-cjk \
      ca-certificates \
      dumb-init \
      curl \
 && rm -rf /var/lib/apt/lists/*

# Non-root user, uid/gid 1000 to match the typical Daytona workspace
# user — avoids chown headaches when the volume is mounted with
# host-owned files.
RUN useradd -m -u 1000 -U -s /bin/bash sf \
 && mkdir -p /data \
 && chown sf:sf /data

# Pre-built binaries from the host. `make linux-binaries-<arch>` (or
# `social-browser provider daytona build` which calls into the same
# cross-compile path) must have populated dist/linux-<arch>/ before
# `docker build` runs, otherwise these COPYs fail.
COPY dist/linux-${TARGETARCH}/social-fetch   /usr/local/bin/social-fetch
COPY dist/linux-${TARGETARCH}/social-ledger  /usr/local/bin/social-ledger
COPY dist/linux-${TARGETARCH}/social-browser /usr/local/bin/social-browser
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh \
 && chmod +x /usr/local/bin/social-fetch \
              /usr/local/bin/social-ledger \
              /usr/local/bin/social-browser

# Ledger persistence root. Compose mounts a named volume here.
WORKDIR /data
USER sf

# Defaults so the three services find each other on loopback without
# the operator having to set anything. Override at run time when
# splitting services across containers.
ENV SOCIAL_FETCH_HEADLESS_EXEC_PATH=/usr/bin/chromium \
    SOCIAL_LEDGER_DIR=/data/ledger \
    SOCIAL_FETCH_HEADLESS_DAEMON_URL=http://127.0.0.1:5556 \
    SOCIAL_LEDGER_DAEMON_URL=http://127.0.0.1:5557

# 5556 = headless browser pool, 5557 = ledger daemon, 5558 = MCP HTTP
EXPOSE 5556 5557 5558

# Periodic check — any one daemon down => container unhealthy. Compose
# / orchestrators react. 30s start-period gives the headless pool time
# to warm 2 chromium processes before the first probe.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD curl -fsS http://127.0.0.1:5556/status >/dev/null \
   && curl -fsS http://127.0.0.1:5557/status >/dev/null \
   && curl -fsS http://127.0.0.1:5558/health >/dev/null \
   || exit 1

ENTRYPOINT ["/usr/bin/dumb-init", "--", "/usr/local/bin/docker-entrypoint.sh"]
CMD ["all"]
