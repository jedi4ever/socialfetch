# socialfetch — build and test targets.
#
# Common workflows:
#   make build         # build the binary into ./bin/socialfetch
#   make test          # run fast (offline) tests
#   make test-live     # run tests that hit real HN, Reddit, GitHub etc.
#   make install       # go install into $GOBIN
#   make run URL=...   # build + run a quick fetch against URL
#   make demo          # fetch a representative URL from each source
#   make clean         # delete ./bin

BIN := ./bin/socialfetch
PKG := ./...
URL ?= https://news.ycombinator.com/news

.PHONY: all build install test test-live test-cover lint vet fmt run demo clean help

all: build

build:
	@mkdir -p bin
	go build -o $(BIN) ./cmd/socialfetch

install:
	go install ./cmd/socialfetch

test:
	go test $(PKG)

test-live:
	go test -tags=live $(PKG) -count=1

test-cover:
	go test -cover $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -s -w .

lint: vet

run: build
	$(BIN) fetch $(URL)

demo: build
	@echo "── HackerNews ──"
	$(BIN) fetch https://news.ycombinator.com/item?id=1 --no-comments
	@echo "\n── GitHub ──"
	$(BIN) fetch https://github.com/golang/go --no-comments
	@echo "\n── Article ──"
	$(BIN) fetch https://example.com/

clean:
	rm -rf bin

help:
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) || \
	  awk '/^[a-zA-Z_-]+:/{ sub(/:.*/, "", $$1); print $$1 }' $(MAKEFILE_LIST) | sort -u
