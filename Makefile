# socialfetch — build and test targets.
# Run 'make' with no arguments to see all available targets.

BIN          := ./dist/socialfetch
SKILL_BIN    := ./skill/socialfetch/scripts/socialfetch
PKG          := ./...
URL          ?= https://news.ycombinator.com/news

# -s strips the symbol table; -w strips DWARF debug info. Together they
# shrink the binary ~40% with no functional loss for a CLI tool — we
# don't ship a debugger. -trimpath removes local filesystem paths so
# builds are reproducible and don't leak the developer's home directory.
GO_BUILD_FLAGS := -ldflags="-s -w" -trimpath
# Override SKILL_INSTALL_DIR to copy the skill to a different location:
#   make skill-install SKILL_INSTALL_DIR=~/.claude/skills/socialfetch
SKILL_INSTALL_DIR ?= $(HOME)/.claude/skills/socialfetch

.PHONY: all help build install test test-live test-cover vet fmt lint run demo clean cli-help \
        skill-build skill-install skill-clean skill-package claude-extension-package extension-validate \
        bridge-package

# Staging dir used when building the redistributable skill zip. Wiped
# before each package run and again after the zip is sealed, so the
# work tree never carries leftover artifacts.
SKILL_PACKAGE_STAGE := $(CURDIR)/dist/.skill-stage

all: help  ## Default target: print this help

help:  ## Show all targets and their purpose
	@printf "socialfetch Makefile\n"
	@printf "\nTargets:\n"
	@awk 'BEGIN{FS=":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nVariables (override on the command line):\n"
	@printf "  URL=<url>       passed to 'make run' (default: %s)\n" "$(URL)"
	@printf "\nQuick start:\n"
	@printf "  make build && ./dist/socialfetch --help\n"
	@printf "  make run URL=https://news.ycombinator.com/item?id=1\n"

build: skill-build  ## Build ./dist/socialfetch and refresh the bundled skill binary

# The skill target depends on every Go source file so the bundled binary
# is rebuilt whenever the implementation changes — guarantees the skill
# can never go stale relative to the working tree.
SKILL_DEPS := $(shell find cmd internal -type f -name '*.go' 2>/dev/null) go.mod go.sum
$(SKILL_BIN): $(SKILL_DEPS)
	@mkdir -p dist $(dir $(SKILL_BIN))
	go build $(GO_BUILD_FLAGS) -o $(BIN) ./cmd/socialfetch
	cp $(BIN) $(SKILL_BIN)

skill-build: $(SKILL_BIN)  ## Build and copy the binary into skill/socialfetch/scripts/

skill-install: skill-build  ## Install the skill into $(SKILL_INSTALL_DIR) (defaults to ~/.claude/skills/socialfetch)
	@mkdir -p $(SKILL_INSTALL_DIR)/scripts
	cp skill/socialfetch/SKILL.md $(SKILL_INSTALL_DIR)/SKILL.md
	cp $(SKILL_BIN) $(SKILL_INSTALL_DIR)/scripts/socialfetch
	@echo "Installed skill to $(SKILL_INSTALL_DIR)"

skill-clean:  ## Uninstall the skill from $(SKILL_INSTALL_DIR) and remove the bundled binary
	@if [ -d "$(SKILL_INSTALL_DIR)" ]; then \
		rm -rf "$(SKILL_INSTALL_DIR)"; \
		echo "Uninstalled skill from $(SKILL_INSTALL_DIR)"; \
	else \
		echo "No skill at $(SKILL_INSTALL_DIR) (already clean)"; \
	fi
	rm -f $(SKILL_BIN)

# extension-package builds a Claude Desktop Extension (.mcpb) for
# darwin/arm64. The .mcpb format is just a zip with a manifest at root
# + the binary at scripts/. Output:
# dist/socialfetch-claude-extension-<version>-darwin-arm64.mcpb.
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

claude-extension-package: extension-validate  ## Package as Claude Desktop Extension (.mcpb) for darwin/arm64
	@rm -rf $(EXTENSION_STAGE)
	@mkdir -p $(EXTENSION_STAGE)/scripts
	GOOS=darwin GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o $(EXTENSION_STAGE)/scripts/socialfetch ./cmd/socialfetch
	@cp mcpb-extension/manifest.json $(EXTENSION_STAGE)/manifest.json
	@VERSION=$$($(EXTENSION_STAGE)/scripts/socialfetch version 2>/dev/null | awk '{print $$2}' || echo unknown); \
	OUT="$(CURDIR)/dist/socialfetch-claude-extension-$$VERSION-darwin-arm64.mcpb"; \
	rm -f "$$OUT"; \
	(cd $(EXTENSION_STAGE) && zip -qr "$$OUT" .); \
	rm -rf $(EXTENSION_STAGE); \
	echo "Packaged: dist/socialfetch-claude-extension-$$VERSION-darwin-arm64.mcpb"

# extension-validate runs Anthropic's official @anthropic-ai/mcpb CLI
# against mcpb-extension/manifest.json. Installed locally via npm
# (node_modules/.bin/mcpb) — no global install required, no shell PATH
# pollution. `npm install` runs automatically the first time the
# binary is missing.
extension-validate:  ## Validate the .mcpb manifest with the official mcpb CLI
	@if [ ! -x "$(MCPB_BIN)" ]; then \
		echo "→ installing local @anthropic-ai/mcpb"; \
		npm install --silent; \
	fi
	@$(MCPB_BIN) validate mcpb-extension/manifest.json

# bridge-package zips the Chrome browser-bridge extension as a
# distributable. The Chrome extension is independent of the
# socialfetch app version — it has its own version field in
# extension/manifest.json which we read at package time.
#
# Output: dist/socialfetch-chrome-extension-<version>.zip. Drop the zip
# into chrome://extensions/ → "Load unpacked" (after unzipping) for
# end users, or distribute via Chrome Web Store after they've set
# up a developer account.
#
# .DS_Store and other macOS junk are filtered out so the zip is
# bit-identical between developers' machines.
bridge-package:  ## Package the Chrome browser-bridge extension as ./dist/socialfetch-chrome-extension-<version>.zip
	@mkdir -p $(CURDIR)/dist
	@VERSION=$$(python3 -c "import json; print(json.load(open('extension/manifest.json'))['version'])"); \
	OUT="$(CURDIR)/dist/socialfetch-chrome-extension-$$VERSION.zip"; \
	rm -f "$$OUT"; \
	(cd extension && zip -qr "$$OUT" . -x "*.DS_Store" "*/.*"); \
	echo "Packaged: dist/socialfetch-chrome-extension-$$VERSION.zip"

# skill-package builds a self-contained zip of the skill ready to
# upload (skills marketplace, file share, attached to a release). The
# archive contains SKILL.md and scripts/socialfetch at the top level
# — same layout `skill-install` expects on disk — so consumers can
# unzip directly into ~/.claude/skills/socialfetch/.
#
# The version string comes from `socialfetch version` so the zip
# filename always tracks the binary's reported version, not a stale
# Makefile constant.
skill-package: skill-build  ## Package the skill as ./dist/socialfetch-skill-<version>.zip
	@rm -rf $(SKILL_PACKAGE_STAGE)
	@mkdir -p $(SKILL_PACKAGE_STAGE)/scripts
	@cp skill/socialfetch/SKILL.md $(SKILL_PACKAGE_STAGE)/SKILL.md
	@cp $(SKILL_BIN) $(SKILL_PACKAGE_STAGE)/scripts/socialfetch
	@VERSION=$$($(BIN) version | awk '{print $$2}'); \
	OUT="$(CURDIR)/dist/socialfetch-skill-$$VERSION.zip"; \
	rm -f "$$OUT"; \
	(cd $(SKILL_PACKAGE_STAGE) && zip -qr "$$OUT" .); \
	rm -rf $(SKILL_PACKAGE_STAGE); \
	echo "Packaged: dist/socialfetch-skill-$$VERSION.zip"

install:  ## go install into $GOBIN
	go install ./cmd/socialfetch

test:  ## Run fast (offline) unit tests
	go test $(PKG)

test-live:  ## Run live tests that hit real network endpoints
	go test -tags=live $(PKG) -count=1

test-cover:  ## Offline tests with coverage report
	go test -cover $(PKG)

vet:  ## go vet
	go vet $(PKG)

fmt:  ## gofmt -s -w .
	gofmt -s -w .

lint: vet  ## Alias for vet

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
