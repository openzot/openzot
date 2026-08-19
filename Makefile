SHELL := /bin/bash
.DEFAULT_GOAL := help

VERSION ?= v$(shell tr -d '[:space:]' < VERSION)
LDFLAGS  = -s -w -X github.com/openzot/openzot/internal/version.Version=$(VERSION)

# go.mod pins the minimum patched toolchain. Some official Go and development
# images export GOTOOLCHAIN=local, which turns a stale patch release into a hard
# failure instead of letting Go fetch the required toolchain. A command-line
# override (for example `make test GOTOOLCHAIN=local`) still takes precedence.
GOTOOLCHAIN = auto
export GOTOOLCHAIN

CMDS = zot zotui
WORKER_ASSET_DIR = internal/zotui/worker/artifacts
WORKER_BUILD_DIR = $(CURDIR)/.local/build

# Cross-compilation defaults to the host, so `make cross` with no arguments
# builds something predictable rather than whatever was last exported.
GOOS   ?= $(shell go env GOHOSTOS)
GOARCH ?= $(shell go env GOHOSTARCH)

.PHONY: help build dev worker-assets image dev-ui vendor-ui clean test race cover cover-check vet lint fmt cross

# Listing the targets rather than assuming one: zot has two build variants that
# differ in what the binary may read from disk, and picking the wrong one
# silently is exactly the confusion this avoids.
help:
	@echo "zot - an automated software factory in a single binary"
	@echo
	@echo "  make build      Build zot and zotui for release ($(GOOS)/$(GOARCH))"
	@echo "  make dev        Build zot and zotui for development - see below"
	@echo "  make worker-assets  Embed Linux Zot workers for sandbox deployment"
	@echo "  make image      Build the lean Zot runtime image"
	@echo "  make dev-ui     Run the browser command center for local development"
	@echo "  make vendor-ui  Refresh pinned third-party UI assets committed to source"
	@echo "  make test       Run the test suite"
	@echo "  make race       Run the test suite under the race detector"
	@echo "  make cover      Report per-package test coverage"
	@echo "  make cover-check  Fail if total coverage is below 90% (CI gate)"
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

build: worker-assets
	@set -e; for cmd in $(CMDS); do \
		echo "Building $$cmd ($(VERSION), release)..."; \
		CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $$cmd ./cmd/$$cmd; \
	done

# A developer build. The only difference is that it will read a .env from the
# working directory, which a released binary must never do - pointing zot at a
# checkout would otherwise be enough to load whatever credentials are lying
# around in it.
dev: worker-assets
	@set -e; for cmd in $(CMDS); do \
		echo "Building $$cmd ($(VERSION), dev - reads .env)..."; \
		CGO_ENABLED=0 go build -tags dev -trimpath -ldflags "$(LDFLAGS)" -o $$cmd ./cmd/$$cmd; \
	done

image:
	docker build --build-arg VERSION=$(VERSION) --tag openzot/zot:local .

worker-assets:
	@mkdir -p "$(WORKER_ASSET_DIR)" "$(WORKER_BUILD_DIR)"
	@set -eu; for arch in amd64 arm64; do \
		echo "Building embedded zot worker ($(VERSION), linux/$$arch)..."; \
		worker_binary="$(WORKER_BUILD_DIR)/zot-linux-$$arch"; \
		worker_asset="$(WORKER_ASSET_DIR)/zot-linux-$$arch.gz"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "$$worker_binary" ./cmd/zot; \
		gzip -n -9 -c "$$worker_binary" > "$$worker_asset.tmp"; \
		mv "$$worker_asset.tmp" "$$worker_asset"; \
	done

# Uses the credential-free fixture by default. ZOT_CONFIG is the make target's
# explicit config input and takes precedence over the Dev Container's ambient
# ZOTUI_CONFIG; the latter remains Zotui's direct-process environment variable.
dev-ui:
	@$(MAKE) --no-print-directory worker-assets
	@ZOTUI_CONFIG="$${ZOT_CONFIG:-$${ZOTUI_CONFIG:-$(CURDIR)/.devcontainer/zotui.dev.yaml}}" \
		ZOTUI_ADDR="$${ZOTUI_ADDR:-127.0.0.1:8080}" \
		ZOTUI_REPO_PATH="$${ZOTUI_REPO_PATH:-$(CURDIR)}" \
		ZOTUI_STORE_DSN="$${ZOTUI_STORE_DSN:-$(CURDIR)/.local/state/zotui.db}" \
		go run ./cmd/zotui

# Downloads checksum-pinned browser dependencies into the embedded asset tree.
# The WOFF2 files are stored through the repository's existing Git LFS rules.
vendor-ui:
	@./scripts/vendor-ui.sh

fmt:
	go fmt ./...

test:
	go test ./... -count=1

race:
	go test -race ./... -count=1

cover:
	@go test -cover ./... -count=1 | grep coverage | sed 's|github.com/openzot/openzot||'

# The coverage gate, shared with CI so local and CI enforce the same number.
# Override the bar with COVERAGE_THRESHOLD=95 make cover-check.
cover-check:
	@./scripts/coverage.sh

# Both variants, because a build tag can break a compile the default never
# reaches - and the developer build is the one no pipeline exercises.
vet:
	go vet ./...
	go vet -tags dev ./...

lint: vet
	@echo "lint ok"

clean:
	rm -f $(CMDS)
	rm -f $(WORKER_ASSET_DIR)/zot-linux-*.gz

# Cross-compile a specific platform: make cross GOOS=darwin GOARCH=arm64
cross: worker-assets
	@set -e; for cmd in $(CMDS); do \
		echo "Building $$cmd ($(VERSION)) for $(GOOS)/$(GOARCH)..."; \
		CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $$cmd ./cmd/$$cmd; \
	done
