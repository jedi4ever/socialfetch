# social-fetch — build and test targets.
# Run 'make' with no arguments to see all available targets.

BIN                     := ./dist/social-fetch
LEDGER_CMD_BIN          := ./dist/social-ledger
BROWSER_CMD_BIN         := ./dist/social-browser
SKILL_BIN               := ./skills/social-fetch/scripts/social-fetch
SKILL_LEDGER_BIN        := ./skills/social-fetch/scripts/social-ledger
LEDGER_SKILL_DIR        := ./skills/social-ledger
LEDGER_SKILL_BIN        := ./skills/social-ledger/scripts/social-ledger
LEDGER_SKILL_INSTALL_DIR ?= $(HOME)/.claude/skills/social-ledger
PKG          := ./...
URL          ?= https://news.ycombinator.com/news

# -s strips the symbol table; -w strips DWARF debug info. Together they
# shrink the binary ~40% with no functional loss for a CLI tool — we
# don't ship a debugger. -trimpath removes local filesystem paths so
# builds are reproducible and don't leak the developer's home directory.
GO_BUILD_FLAGS := -ldflags="-s -w" -trimpath
# Override SKILL_INSTALL_DIR to copy the skill to a different location:
#   make skill-install SKILL_INSTALL_DIR=~/.claude/skills/social-fetch
SKILL_INSTALL_DIR ?= $(HOME)/.claude/skills/social-fetch

.PHONY: all help build install test test-integration test-live test-cover vet fmt lint run demo clean cli-help \
        check install-hooks \
        skill-build skill-install skill-clean skill-package claude-desktop-extension-package extension-validate \
        bridge-package plugin-build plugin-package gh-sync-secrets gh-sync-secrets-dry \
        ledger-build ledger-test \
        ledger-skill-build ledger-skill-install ledger-skill-clean ledger-skill-package \
        docker-build docker-build-amd64 docker-build-arm64 \
        linux-binaries linux-binaries-amd64 linux-binaries-arm64 \
        docker-run docker-compose-up docker-compose-down docker-shell

# Staging dir used when building the redistributable skill zip. Wiped
# before each package run and again after the zip is sealed, so the
# work tree never carries leftover artifacts.
SKILL_PACKAGE_STAGE := $(CURDIR)/dist/.skill-stage

all: help  ## Default target: print this help

help:  ## Show all targets and their purpose
	@printf "social-fetch Makefile\n"
	@printf "\nTargets:\n"
	@awk 'BEGIN{FS=":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nVariables (override on the command line):\n"
	@printf "  URL=<url>       passed to 'make run' (default: %s)\n" "$(URL)"
	@printf "\nQuick start:\n"
	@printf "  make build && ./dist/social-fetch --help\n"
	@printf "  make run URL=https://news.ycombinator.com/item?id=1\n"

build: skill-build  ## Build ./dist/social-fetch and refresh the bundled skill binary

# The skill target depends on every Go source file so the bundled binary
# is rebuilt whenever the implementation changes — guarantees the skill
# can never go stale relative to the working tree. Both the parent
# social-fetch binary and social-ledger ride along; the auto-detect
# in `internal/ledger.binaryPath()` finds the ledger as a sibling of
# social-fetch in scripts/, so the skill gets ledger functionality
# without any env-var setup on the user's side.
SKILL_DEPS := $(shell find cmd internal -type f -name '*.go' 2>/dev/null) go.mod go.sum
$(SKILL_BIN): $(SKILL_DEPS)
	@mkdir -p dist $(dir $(SKILL_BIN))
	go build $(GO_BUILD_FLAGS) -o $(BIN) ./cmd/social-fetch
	go build $(GO_BUILD_FLAGS) -o $(LEDGER_CMD_BIN) ./cmd/social-ledger
	go build $(GO_BUILD_FLAGS) -o $(BROWSER_CMD_BIN) ./cmd/social-browser
	cp $(BIN) $(SKILL_BIN)
	cp $(LEDGER_CMD_BIN) $(SKILL_LEDGER_BIN)

skill-build: $(SKILL_BIN)  ## Build both binaries and copy into skills/social-fetch/scripts/

skill-install: skill-build  ## Install the skill into $(SKILL_INSTALL_DIR) (defaults to ~/.claude/skills/social-fetch)
	@mkdir -p $(SKILL_INSTALL_DIR)/scripts
	cp skills/social-fetch/SKILL.md $(SKILL_INSTALL_DIR)/SKILL.md
	cp $(SKILL_BIN) $(SKILL_INSTALL_DIR)/scripts/social-fetch
	cp $(SKILL_LEDGER_BIN) $(SKILL_INSTALL_DIR)/scripts/social-ledger
	@echo "Installed skill to $(SKILL_INSTALL_DIR)"

skill-clean:  ## Uninstall the skill from $(SKILL_INSTALL_DIR) and remove the bundled binaries
	@if [ -d "$(SKILL_INSTALL_DIR)" ]; then \
		rm -rf "$(SKILL_INSTALL_DIR)"; \
		echo "Uninstalled skill from $(SKILL_INSTALL_DIR)"; \
	else \
		echo "No skill at $(SKILL_INSTALL_DIR) (already clean)"; \
	fi
	rm -f $(SKILL_BIN) $(SKILL_LEDGER_BIN)

# extension-package builds a Claude Desktop Extension (.mcpb) for
# darwin/arm64. The .mcpb format is just a zip with a manifest at root
# + the binary at scripts/. Output:
# dist/social-skills-claude-desktop-extension-<version>-darwin-arm64.mcpb.
#
# Phase 1 is darwin/arm64 only (the developer's platform). Phase 2
# will fan this out to darwin-amd64 / linux-amd64 / windows-amd64 via
# additional targets — Go cross-compilation is one GOOS=… GOARCH=…
# go build per target.
#
# Depends on extension-validate so the build fails fast if someone
# adds a manifest field that breaks the schema.
EXTENSION_STAGE := $(CURDIR)/dist/.extension-stage
MCPB_BIN        := ./node_modules/.bin/mcpb

claude-desktop-extension-package: extension-validate  ## Package as Claude Desktop Extension (.mcpb) for darwin/arm64
	@rm -rf $(EXTENSION_STAGE)
	@mkdir -p $(EXTENSION_STAGE)/scripts
	GOOS=darwin GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o $(EXTENSION_STAGE)/scripts/social-fetch ./cmd/social-fetch
	GOOS=darwin GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o $(EXTENSION_STAGE)/scripts/social-ledger ./cmd/social-ledger
	@cp extensions/claude-desktop/manifest.json $(EXTENSION_STAGE)/manifest.json
	@VERSION=$$(awk -F'"' '/"version":/ {print $$4; exit}' extensions/claude-desktop/manifest.json); \
	OUT="$(CURDIR)/dist/social-skills-claude-desktop-extension-$$VERSION-darwin-arm64.mcpb"; \
	rm -f "$$OUT"; \
	(cd $(EXTENSION_STAGE) && zip -qr "$$OUT" .); \
	rm -rf $(EXTENSION_STAGE); \
	echo "Packaged: dist/social-skills-claude-desktop-extension-$$VERSION-darwin-arm64.mcpb"

# extension-validate runs Anthropic's official @anthropic-ai/mcpb CLI
# against extensions/claude-desktop/manifest.json. Installed locally via npm
# (node_modules/.bin/mcpb) — no global install required, no shell PATH
# pollution. `npm install` runs automatically the first time the
# binary is missing.
extension-validate:  ## Validate the .mcpb manifest with the official mcpb CLI
	@if [ ! -x "$(MCPB_BIN)" ]; then \
		echo "→ installing local @anthropic-ai/mcpb"; \
		npm install --silent; \
	fi
	@$(MCPB_BIN) validate extensions/claude-desktop/manifest.json

# bridge-package zips the Chrome browser-bridge extension as a
# distributable. The Chrome extension is independent of the
# social-fetch app version — it has its own version field in
# extensions/chrome/manifest.json which we read at package time.
#
# Output: dist/social-skills-chrome-extension-<version>.zip. Drop the zip
# into chrome://extensions/ → "Load unpacked" (after unzipping) for
# end users, or distribute via Chrome Web Store after they've set
# up a developer account.
#
# .DS_Store and other macOS junk are filtered out so the zip is
# bit-identical between developers' machines.
bridge-package:  ## Package the Chrome browser-bridge extension as ./dist/social-skills-chrome-extension-<version>.zip
	@mkdir -p $(CURDIR)/dist
	@VERSION=$$(python3 -c "import json; print(json.load(open('extensions/chrome/manifest.json'))['version'])"); \
	OUT="$(CURDIR)/dist/social-skills-chrome-extension-$$VERSION.zip"; \
	rm -f "$$OUT"; \
	(cd extensions/chrome && zip -qr "$$OUT" . -x "*.DS_Store" "*/.*"); \
	echo "Packaged: dist/social-skills-chrome-extension-$$VERSION.zip"

# ---- docker container -----------------------------------------------------
# The container runs all three long-running services (headless browser
# pool :5556, ledger daemon :5557, MCP HTTP server :5558) under a tiny
# supervisor in docker-entrypoint.sh.
#
# Build model (v0.15.4+): Go binaries are cross-compiled on the host
# to dist/linux-<arch>/, then the Dockerfile single-stage COPYs them
# in. Switching between arm64 (apple-silicon dev) and amd64 (Daytona)
# reuses the apt-install layer and re-runs the Go build only when
# source changes. Pre-v0.15.4 used a builder stage with `go build`
# inside docker; that re-built Go from scratch on every arch flip
# because Docker layer cache is per-platform.
#
# Targets:
#   docker-build         host-native arch (auto-detect; arm64 on apple silicon)
#   docker-build-arm64   apple-silicon local-dev image
#   docker-build-amd64   Daytona-target image
#   linux-binaries-<arch>  Go cross-compile only, no docker
#
# `docker-run` uses :latest. `docker-compose-up` is the dev shorthand —
# use it when you want to point Claude Desktop / claude.ai at the
# local container.
DOCKER_IMAGE     = social-skills
DOCKER_VERSION   = $(shell awk -F\" '/^const Version =/ {print $$2; exit}' cmd/social-fetch/main.go)
DOCKER_LEDGER_VOL = social-skills-ledger

# Cross-compile artifacts. Per-arch dirs so we can build both in
# parallel without clobbering. Listed in their own variable so the
# `docker-build-<arch>` rules and the daemon's
# `provider daytona build` Go path stay in sync about WHERE the
# binaries land.
LINUX_BINS              := social-fetch social-ledger social-browser
LINUX_BIN_DIR_AMD64     := dist/linux-amd64
LINUX_BIN_DIR_ARM64     := dist/linux-arm64
LINUX_BINS_AMD64        := $(addprefix $(LINUX_BIN_DIR_AMD64)/,$(LINUX_BINS))
LINUX_BINS_ARM64        := $(addprefix $(LINUX_BIN_DIR_ARM64)/,$(LINUX_BINS))

# Per-binary, per-arch build rules. Pattern stem is the cmd dir
# under cmd/ (e.g. "social-browser") which matches the binary name.
$(LINUX_BIN_DIR_AMD64)/%: $(SKILL_DEPS)
	@mkdir -p $(LINUX_BIN_DIR_AMD64)
	GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -o $@ ./cmd/$*

$(LINUX_BIN_DIR_ARM64)/%: $(SKILL_DEPS)
	@mkdir -p $(LINUX_BIN_DIR_ARM64)
	GOOS=linux GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o $@ ./cmd/$*

linux-binaries-amd64: $(LINUX_BINS_AMD64)  ## Cross-compile linux/amd64 binaries (Daytona target)
linux-binaries-arm64: $(LINUX_BINS_ARM64)  ## Cross-compile linux/arm64 binaries (apple-silicon local docker)
linux-binaries: linux-binaries-amd64 linux-binaries-arm64  ## Both archs

# `docker buildx` needed for --platform + --load. Plain `docker build`
# would also work but buildx is cleaner about cross-arch context.
docker-build-amd64: linux-binaries-amd64  ## Build social-skills:<version> for linux/amd64 (Daytona-bound)
	docker buildx build --platform linux/amd64 \
	  -t $(DOCKER_IMAGE):$(DOCKER_VERSION) \
	  -t $(DOCKER_IMAGE):latest \
	  --load .

docker-build-arm64: linux-binaries-arm64  ## Build social-skills:<version> for linux/arm64 (apple-silicon dev)
	docker buildx build --platform linux/arm64 \
	  -t $(DOCKER_IMAGE):$(DOCKER_VERSION) \
	  -t $(DOCKER_IMAGE):latest \
	  --load .

# Default `docker-build` picks the host's native arch. uname -m on
# apple silicon returns "arm64"; on linux/amd64 hosts it returns
# "x86_64". Anything else falls through to amd64 — the safer default
# for unknown CI runners.
DOCKER_HOST_ARCH := $(shell uname -m)
ifeq ($(DOCKER_HOST_ARCH),arm64)
docker-build: docker-build-arm64  ## Build social-skills container for the host's native arch
else ifeq ($(DOCKER_HOST_ARCH),aarch64)
docker-build: docker-build-arm64
else
docker-build: docker-build-amd64
endif

docker-run:  ## Run the container with all three daemons exposed and a named volume for state
	docker run --rm -it \
	  --name social-skills \
	  -p 5556:5556 -p 5557:5557 -p 5558:5558 \
	  -v $(DOCKER_LEDGER_VOL):/data \
	  --shm-size=1g \
	  $(DOCKER_IMAGE):latest

docker-compose-up:  ## Bring up the stack via docker compose (rebuilds image)
	docker compose up --build

docker-compose-down:  ## Stop the compose stack (preserves the named volume)
	docker compose down

docker-shell:  ## Drop into a running container's shell (assumes name=social-skills)
	docker exec -it social-skills /bin/sh

# skill-package builds a self-contained zip of the skill ready to
# upload (skills marketplace, file share, attached to a release). The
# archive contains SKILL.md and scripts/social-fetch at the top level
# — same layout `skill-install` expects on disk — so consumers can
# unzip directly into ~/.claude/skills/social-fetch/.
#
# The version string comes from `social-fetch version` so the zip
# filename always tracks the binary's reported version, not a stale
# Makefile constant.
# social-ledger ships as a separate skill at
# skills/social-ledger/ — its own SKILL.md scoped to the
# ledger subcommands (seen, get, list, search, record, …) so an
# agent that wants ledger access doesn't have to load the full
# social-fetch fetch surface. Bundles the same binary as the
# main skill (the binary is fungible — the skills differ only
# in SKILL.md and which subcommands the agent is allowed to
# invoke).
LEDGER_SKILL_PACKAGE_STAGE := $(CURDIR)/dist/.ledger-skill-stage

ledger-skill-build: $(LEDGER_SKILL_BIN)  ## Build social-ledger and copy into skills/social-ledger/scripts/
$(LEDGER_SKILL_BIN): $(SKILL_DEPS)
	@mkdir -p $(dir $(LEDGER_SKILL_BIN)) dist
	go build $(GO_BUILD_FLAGS) -o $(LEDGER_CMD_BIN) ./cmd/social-ledger
	cp $(LEDGER_CMD_BIN) $(LEDGER_SKILL_BIN)

ledger-skill-install: ledger-skill-build  ## Install social-ledger skill into $(LEDGER_SKILL_INSTALL_DIR)
	@mkdir -p $(LEDGER_SKILL_INSTALL_DIR)/scripts
	cp $(LEDGER_SKILL_DIR)/SKILL.md $(LEDGER_SKILL_INSTALL_DIR)/SKILL.md
	cp $(LEDGER_SKILL_BIN) $(LEDGER_SKILL_INSTALL_DIR)/scripts/social-ledger
	@echo "Installed ledger skill to $(LEDGER_SKILL_INSTALL_DIR)"

ledger-skill-clean:  ## Uninstall the ledger skill
	@if [ -d "$(LEDGER_SKILL_INSTALL_DIR)" ]; then \
		rm -rf "$(LEDGER_SKILL_INSTALL_DIR)"; \
		echo "Uninstalled ledger skill from $(LEDGER_SKILL_INSTALL_DIR)"; \
	else \
		echo "No ledger skill at $(LEDGER_SKILL_INSTALL_DIR) (already clean)"; \
	fi
	rm -f $(LEDGER_SKILL_BIN)

ledger-skill-package: ledger-skill-build  ## Package the ledger skill as ./dist/social-ledger-skill-<version>.zip
	@rm -rf $(LEDGER_SKILL_PACKAGE_STAGE)
	@mkdir -p $(LEDGER_SKILL_PACKAGE_STAGE)/scripts
	@cp $(LEDGER_SKILL_DIR)/SKILL.md $(LEDGER_SKILL_PACKAGE_STAGE)/SKILL.md
	@cp $(LEDGER_SKILL_BIN) $(LEDGER_SKILL_PACKAGE_STAGE)/scripts/social-ledger
	@VERSION=$$($(BIN) version | awk '{print $$2}'); \
	OUT="$(CURDIR)/dist/social-ledger-skill-$$VERSION.zip"; \
	rm -f "$$OUT"; \
	(cd $(LEDGER_SKILL_PACKAGE_STAGE) && zip -qr "$$OUT" .); \
	rm -rf $(LEDGER_SKILL_PACKAGE_STAGE); \
	echo "Packaged: dist/social-ledger-skill-$$VERSION.zip"

skill-package: skill-build  ## Package the skill as ./dist/social-fetch-skill-<version>.zip
	@rm -rf $(SKILL_PACKAGE_STAGE)
	@mkdir -p $(SKILL_PACKAGE_STAGE)/scripts
	@cp skills/social-fetch/SKILL.md $(SKILL_PACKAGE_STAGE)/SKILL.md
	@cp $(SKILL_BIN) $(SKILL_PACKAGE_STAGE)/scripts/social-fetch
	@cp $(SKILL_LEDGER_BIN) $(SKILL_PACKAGE_STAGE)/scripts/social-ledger
	@VERSION=$$($(BIN) version | awk '{print $$2}'); \
	OUT="$(CURDIR)/dist/social-fetch-skill-$$VERSION.zip"; \
	rm -f "$$OUT"; \
	(cd $(SKILL_PACKAGE_STAGE) && zip -qr "$$OUT" .); \
	rm -rf $(SKILL_PACKAGE_STAGE); \
	echo "Packaged: dist/social-fetch-skill-$$VERSION.zip"

# plugin-build regenerates the plugin's SKILL.md from skills/social-fetch/SKILL.md
# with `scripts/social-fetch` rewritten to bare `social-fetch`. The plugin
# assumes the binary is already on PATH (Claude Code plugins don't
# auto-install dependencies); see extensions/claude-code/README.md.
#
# We commit the generated SKILL.md so `/plugin marketplace add jedi4ever/social-skills`
# works without a build step on the consumer side. Run this target whenever
# skills/social-fetch/SKILL.md changes — CLAUDE.md "lockstep" rule.
PLUGIN_DIR    := extensions/claude-code
PLUGIN_SKILL  := $(PLUGIN_DIR)/skills/social-fetch/SKILL.md
SKILL_SOURCE  := skills/social-fetch/SKILL.md

plugin-build:  ## Regenerate extensions/claude-code/skills/social-fetch/SKILL.md from skills/social-fetch/SKILL.md
	@mkdir -p $(dir $(PLUGIN_SKILL))
	sed -E 's|scripts/social-fetch|social-fetch|g; s|the `social-fetch` Go binary on PATH \(install separately — see the plugin README\)|the `social-fetch` Go binary on PATH (install separately — see the plugin README)|; s|Wraps the `social-fetch` Go binary at `social-fetch` \(relative to this skill\)\.|Wraps the `social-fetch` Go binary on the user'"'"'s PATH (install separately — see the plugin README).|' $(SKILL_SOURCE) > $(PLUGIN_SKILL)
	@echo "Regenerated $(PLUGIN_SKILL)"

plugin-package: plugin-build  ## Package the Claude Code plugin as ./dist/social-skills-claude-code-plugin-<version>.zip
	@mkdir -p $(CURDIR)/dist
	@VERSION=$$(awk -F'"' '/"version":/ {print $$4; exit}' $(PLUGIN_DIR)/.claude-plugin/plugin.json); \
	OUT="$(CURDIR)/dist/social-skills-claude-code-plugin-$$VERSION.zip"; \
	rm -f "$$OUT"; \
	(cd $(PLUGIN_DIR) && zip -qr "$$OUT" . -x "*.DS_Store" "bin/*" "node_modules/*"); \
	echo "Packaged: dist/social-skills-claude-code-plugin-$$VERSION.zip"

gh-sync-secrets-dry:  ## Preview which .env keys would be uploaded to GitHub Actions secrets
	@DRY_RUN=1 ./scripts/gh-sync-secrets.sh

gh-sync-secrets:  ## Push API keys from .env to GitHub Actions secrets (powers .github/workflows/live.yml)
	@./scripts/gh-sync-secrets.sh

# social-ledger is the second binary in this module — same
# `go.mod`, separate `cmd/` entry point. Imports the SQLite-backed
# packages under internal/ledger/{store,mirror,item}; the parent
# social-fetch binary doesn't link those, so its size / dep tree
# stays unaffected.
LEDGER_BIN := ./dist/social-ledger
ledger-build:  ## Build dist/social-ledger
	@mkdir -p dist
	go build $(GO_BUILD_FLAGS) -o $(LEDGER_BIN) ./cmd/social-ledger

ledger-test:  ## Run the ledger sub-package tests only
	go test ./internal/ledger/... ./cmd/social-ledger/... -count=1

install:  ## go install into $GOBIN (both binaries)
	go install ./cmd/social-fetch ./cmd/social-ledger

test:  ## Run fast (offline) unit tests
	go test $(PKG)

test-integration:  ## Run integration tests (build tag `integration`)
	go test -tags=integration ./cmd/social-fetch/ -count=1

test-live:  ## Run live tests that hit real network endpoints
	go test -tags=live $(PKG) -count=1

test-cover:  ## Offline tests with coverage report
	go test -cover $(PKG)

vet:  ## go vet
	go vet $(PKG)

fmt:  ## gofmt -s -w .
	gofmt -s -w .

lint: vet  ## Alias for vet

# `make check` is the pre-commit gate — runs the same battery the
# CI workflow runs. Always invoke this before `git commit` to
# avoid pushing breakage that the CI catches on the next run.
#   1. gofmt -l . must be empty (CI step `gofmt`)
#   2. go vet ./... must be silent (CI step `go vet`)
#   3. go test ./... must pass (CI step `go test (offline)`)
#   4. plugin SKILL.md must already be regenerated (CI step
#      `Plugin SKILL.md is in sync` runs make plugin-build and
#      git diff --exit-code)
check:  ## Run the same checks CI runs (gofmt + vet + test + plugin SKILL.md sync)
	@set -euo pipefail; \
	out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "::error::gofmt issues — run 'make fmt'"; \
		echo "$$out"; \
		exit 1; \
	fi; \
	echo "✓ gofmt clean"
	@go vet ./...
	@echo "✓ go vet clean"
	@go test ./... -count=1 >/dev/null
	@echo "✓ go test passing"
	@$(MAKE) -s plugin-build >/dev/null
	@if ! git diff --exit-code extensions/claude-code/skills/social-fetch/SKILL.md >/dev/null 2>&1; then \
		echo "::error::extensions/claude-code/skills/social-fetch/SKILL.md is stale — commit the regenerated file"; \
		exit 1; \
	fi
	@echo "✓ plugin SKILL.md in sync"
	@set -e; \
	bin_ver=$$(awk -F\" '/^const Version =/ {print $$2; exit}' cmd/social-fetch/main.go); \
	ledger_ver=$$(awk -F\" '/^const Version =/ {print $$2; exit}' cmd/social-ledger/main.go); \
	browser_ver=$$(awk -F\" '/^const Version =/ {print $$2; exit}' cmd/social-browser/main.go); \
	desktop_ver=$$(awk -F\" '/^  "version":/ {print $$4; exit}' extensions/claude-desktop/manifest.json); \
	plugin_ver=$$(awk -F\" '/^  "version":/ {print $$4; exit}' extensions/claude-code/.claude-plugin/plugin.json); \
	market_ver=$$(awk -F\" '/^  "version":/ {print $$4; exit}' .claude-plugin/marketplace.json); \
	mismatch=""; \
	[ -n "$$bin_ver" ] || mismatch="$$mismatch  - cmd/social-fetch/main.go: Version constant missing\n"; \
	[ "$$ledger_ver" = "$$bin_ver" ] || mismatch="$$mismatch  - cmd/social-ledger/main.go: $$ledger_ver (want $$bin_ver)\n"; \
	[ "$$browser_ver" = "$$bin_ver" ] || mismatch="$$mismatch  - cmd/social-browser/main.go: $$browser_ver (want $$bin_ver)\n"; \
	[ "$$desktop_ver" = "$$bin_ver" ] || mismatch="$$mismatch  - extensions/claude-desktop/manifest.json: $$desktop_ver (want $$bin_ver)\n"; \
	[ "$$plugin_ver" = "$$bin_ver" ] || mismatch="$$mismatch  - extensions/claude-code/.claude-plugin/plugin.json: $$plugin_ver (want $$bin_ver)\n"; \
	[ "$$market_ver" = "$$bin_ver" ] || mismatch="$$mismatch  - .claude-plugin/marketplace.json: $$market_ver (want $$bin_ver)\n"; \
	if [ -n "$$mismatch" ]; then \
		echo "::error::version lockstep violation — these six must all match $$bin_ver:"; \
		printf "$$mismatch"; \
		echo "  see CLAUDE.md \"Versioning\" — bump all together"; \
		exit 1; \
	fi
	@echo "✓ versions in lockstep ($(shell awk -F\" '/^const Version =/ {print $$2; exit}' cmd/social-fetch/main.go))"
	@echo ""
	@echo "all checks passed — safe to commit"

install-hooks:  ## Install the .githooks/* git hooks (one-time, per clone)
	@git config core.hooksPath .githooks
	@echo "✓ git hooks active — pre-commit will run 'make check' on every commit"
	@echo "  bypass with 'git commit --no-verify' when needed"

run: build  ## Build and fetch URL (override with URL=...)
	$(BIN) fetch $(URL)

demo: build  ## Fetch a representative URL from each source
	@echo "── HackerNews ──"
	$(BIN) fetch https://news.ycombinator.com/item?id=1 --no-comments
	@echo "\n── GitHub ──"
	$(BIN) fetch https://github.com/golang/go --no-comments
	@echo "\n── Article ──"
	$(BIN) fetch https://example.com/

cli-help: build  ## Print the CLI's full --help
	$(BIN) --help

clean: skill-clean  ## Delete ./dist and the skill binary
	rm -rf dist bin
