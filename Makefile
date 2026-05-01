# socialfetch — build and test targets.
# Run 'make' with no arguments to see all available targets.

BIN := ./bin/socialfetch
PKG := ./...
URL ?= https://news.ycombinator.com/news

.PHONY: all help build install test test-live test-cover vet fmt lint run demo clean cli-help

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

build:  ## Build ./bin/socialfetch
	@mkdir -p bin
	go build -o $(BIN) ./cmd/socialfetch

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

clean:  ## Delete ./bin
	rm -rf bin
