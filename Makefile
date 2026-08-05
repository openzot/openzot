SHELL := /bin/bash
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X github.com/openzot/openzot/internal/version.Version=$(VERSION)

CMDS = zot

# Cross-compilation defaults to the host, so `make cross` with no arguments
# builds something predictable rather than whatever was last exported.
GOOS   ?= $(shell go env GOHOSTOS)
GOARCH ?= $(shell go env GOHOSTARCH)

.PHONY: help build dev clean test race cover vet lint fmt cross

# Listing the targets rather than assuming one: zot has two build variants that
# differ in what the binary may read from disk, and picking the wrong one
# silently is exactly the confusion this avoids.
help:
	@echo "zot - an automated software factory in a single binary"
	@echo
	@echo "  make build      Build ./zot for release ($(GOOS)/$(GOARCH))"
	@echo "  make dev        Build ./zot for development - see below"
	@echo "  make test       Run the test suite"
	@echo "  make race       Run the test suite under the race detector"
	@echo "  make cover      Report per-package test coverage"
	@echo "  make vet        Run go vet over both build variants"
	@echo "  make fmt        Format the tree"
	@echo "  make lint       Alias for vet"
	@echo "  make cross      Cross-compile: make cross GOOS=darwin GOARCH=arm64"
	@echo "  make clean      Remove built binaries"
	@echo
	@echo "build vs dev: a release binary does NOT read a .env from its working"
	@echo "  directory; a developer build does. zot runs unattended with a"
	@echo "  provider key and a shell tool, so a released binary must not take"
	@echo "  credentials from whatever directory it was pointed at. Both write"
	@echo "  to ./zot - run './zot --version' to see which one you have."
	@echo
	@echo "Overrides: VERSION=$(VERSION)"
	@echo "           GOOS=$(GOOS) GOARCH=$(GOARCH)"

build:
	@for cmd in $(CMDS); do \
		echo "Building $$cmd ($(VERSION), release)..."; \
		CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $$cmd ./cmd/$$cmd; \
	done

# A developer build. The only difference is that it will read a .env from the
# working directory, which a released binary must never do - pointing zot at a
# checkout would otherwise be enough to load whatever credentials are lying
# around in it.
dev:
	@for cmd in $(CMDS); do \
		echo "Building $$cmd ($(VERSION), dev - reads .env)..."; \
		CGO_ENABLED=0 go build -tags dev -trimpath -ldflags "$(LDFLAGS)" -o $$cmd ./cmd/$$cmd; \
	done

fmt:
	go fmt ./...

test:
	go test ./... -count=1

race:
	go test -race ./... -count=1

cover:
	@go test -cover ./... -count=1 | grep coverage | sed 's|github.com/openzot/openzot||'

# Both variants, because a build tag can break a compile the default never
# reaches - and the developer build is the one no pipeline exercises.
vet:
	go vet ./...
	go vet -tags dev ./...

lint: vet
	@echo "lint ok"

clean:
	rm -f $(CMDS)

# Cross-compile a specific platform: make cross GOOS=darwin GOARCH=arm64
cross:
	@for cmd in $(CMDS); do \
		echo "Building $$cmd ($(VERSION)) for $(GOOS)/$(GOARCH)..."; \
		CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $$cmd ./cmd/$$cmd; \
	done
