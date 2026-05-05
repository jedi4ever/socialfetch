# syntax=docker/dockerfile:1.7
#
# social-skills container — runs the three long-running services
# (headless browser pool, ledger daemon, MCP over HTTP) in one image
# so a single `docker compose up` brings up a working stack a remote
# Claude Desktop / claude.ai / Daytona-tunneled agent can point at.
#
# Multi-stage so the runtime layer doesn't carry the Go toolchain.
# Builder produces ./dist/social-fetch + ./dist/social-ledger;
# runtime only carries those binaries plus chromium + a handful of
# fonts.

# === Stage 1: build the binaries ===========================================
FROM golang:1.23-bookworm AS builder

WORKDIR /src

# Cache the module download in its own layer so source-only edits
# don't re-run go mod download.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build flags match the Makefile: stripped + trimpath for reproducible
# binaries. Output goes to /src/dist/{social-fetch, social-ledger}
# which the runtime stage copies from.
RUN go build -ldflags="-s -w" -trimpath -o ./dist/social-fetch ./cmd/social-fetch \
 && go build -ldflags="-s -w" -trimpath -o ./dist/social-ledger ./cmd/social-ledger

# === Stage 2: runtime ======================================================
FROM debian:bookworm-slim AS runtime

# chromium — what chromedp drives via CDP. We don't install
# chromium-driver because we use CDP, not WebDriver.
# fonts-liberation + fonts-noto-cjk — patai's tests show missing
# glyphs trigger anti-bot detection on Asian-language sites; small
# size, big quality win.
# ca-certificates — chromium needs a working trust store for HTTPS.
# dumb-init — PID 1 reaper for clean SIGTERM propagation under
# `docker stop`.
# curl — the HEALTHCHECK + ad-hoc debugging probe.
# tini — alternative reaper (fallback if dumb-init dies); cheap to
# include and idiomatic on slim debian images.
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

COPY --from=builder /src/dist/social-fetch  /usr/local/bin/social-fetch
COPY --from=builder /src/dist/social-ledger /usr/local/bin/social-ledger
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

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
