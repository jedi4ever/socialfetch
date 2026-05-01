# socialfetch — build and test targets.
# Run 'make' with no arguments to see all available targets.

BIN          := ./bin/socialfetch
SKILL_BIN    := ./skill/socialfetch/scripts/socialfetch
PKG          := ./...
URL          ?= https://news.ycombinator.com/news
# Override INSTALL_SKILL_DIR to copy the skill to a different location:
#   make install-skill INSTALL_SKILL_DIR=~/.claude/skills/socialfetch
INSTALL_SKILL_DIR ?= $(HOME)/.claude/skills/socialfetch

.PHONY: all help build install test test-live test-cover vet fmt lint run demo clean cli-help \
        skill skill-clean install-skill

all: help  ## Default target: print this help

help:  ## Show all targets and their purpose
	@printf "socialfetch Makefile\n"
	@printf "\nTargets:\n"
	@awk 'BEGIN{FS=":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nVariables (override on the command line):\n"
	@printf "  URL=<url>       passed to 'make run' (default: %s)\n" "$(URL)"
	@printf "\nQuick start:\n"
	@printf "  make build && ./bin/socialfetch --help\n"
	@printf "  make run URL=https://news.ycombinator.com/item?id=1\n"

build: skill  ## Build ./bin/socialfetch and refresh the bundled skill binary

# The skill target depends on every Go source file so the bundled binary
# is rebuilt whenever the implementation changes — guarantees the skill
# can never go stale relative to the working tree.
SKILL_DEPS := $(shell find cmd internal -type f -name '*.go' 2>/dev/null) go.mod go.sum
$(SKILL_BIN): $(SKILL_DEPS)
	@mkdir -p bin $(dir $(SKILL_BIN))
	go build -o $(BIN) ./cmd/socialfetch
	cp $(BIN) $(SKILL_BIN)

skill: $(SKILL_BIN)  ## Build and copy the binary into skill/socialfetch/scripts/

skill-clean:  ## Remove the bundled skill binary
	rm -f $(SKILL_BIN)

install-skill: skill  ## Copy the skill into $(INSTALL_SKILL_DIR) (defaults to ~/.claude/skills/socialfetch)
	@mkdir -p $(INSTALL_SKILL_DIR)/scripts
	cp skill/socialfetch/SKILL.md $(INSTALL_SKILL_DIR)/SKILL.md
	cp $(SKILL_BIN) $(INSTALL_SKILL_DIR)/scripts/socialfetch
	@echo "Installed skill to $(INSTALL_SKILL_DIR)"

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

clean: skill-clean  ## Delete ./bin and the skill binary
	rm -rf bin
