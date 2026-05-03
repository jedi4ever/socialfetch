# socialfetch-ledger — build and test targets.
#
# Separate Makefile so this module can be lifted into its own repo
# (jedi4ever/socialfetch-ledger) without dragging socialfetch's
# build infrastructure along. The parent repo's Makefile has a
# 'ledger-build' convenience target that delegates here.

BIN := ./dist/socialfetch-ledger

# -s / -w strip the symbol table and DWARF for a smaller static
# binary; -trimpath wipes local filesystem paths so builds are
# reproducible. Same flags as socialfetch's main Makefile.
GO_BUILD_FLAGS := -ldflags="-s -w" -trimpath

.PHONY: all help build install test test-race vet fmt clean

all: help

help:  ## Print all targets and their purpose
	@printf "socialfetch-ledger Makefile\n\nTargets:\n"
	@awk 'BEGIN{FS=":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build:  ## Build ./dist/socialfetch-ledger
	@mkdir -p dist
	go build $(GO_BUILD_FLAGS) -o $(BIN) ./cmd/socialfetch-ledger
	@echo "Built: $(BIN)"

install:  ## go install into $GOBIN
	go install $(GO_BUILD_FLAGS) ./cmd/socialfetch-ledger

test:  ## Run unit tests (offline)
	go test ./...

test-race:  ## Run unit tests under the race detector
	go test -race ./...

vet:  ## go vet
	go vet ./...

fmt:  ## gofmt -s -w .
	gofmt -s -w .

clean:  ## Remove ./dist
	rm -rf dist
