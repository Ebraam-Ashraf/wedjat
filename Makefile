SHELL := /bin/bash

# Define paths
ROOT_DIR := $(abspath $(CURDIR))
DAEMON_DIR := $(ROOT_DIR)/daemon
UI_DIR := $(ROOT_DIR)/ui
DIST_DIR := $(ROOT_DIR)/dist
STAGE_DIR := $(DIST_DIR)/.stage

GOARCH ?= amd64
RELEASE_NAME := wedjat-linux-$(GOARCH)
ARCHIVE := $(DIST_DIR)/$(RELEASE_NAME).tar.gz

.DEFAULT_GOAL := build

.PHONY: build release install uninstall test clean help dev dev-daemon dev-ui fmt lint check ci

help:
	@printf '%s\n' \
		'Wedjat developer commands' \
		'  make release      Build the release artifacts (daemon + ui + cli) into dist/' \
		'  sudo make install Install an existing local release from dist/' \
		'  sudo make uninstall Uninstall Wedjat (calls wedjat uninstall)' \
		'  make test         Run all unit tests across the repo' \
		'  make clean        Delete build artifacts only (never uninstalls system)' \
		'  make dev          Run both dev daemon and dev UI together' \
		'  make dev-daemon   Build and run dev daemon in foreground' \
		'  make dev-ui       Run dev UI server (with HMR) in foreground' \
		'  make fmt          Format Go and C source code' \
		'  make check        Verify required build tools are installed' \
		'  make help         Show this help'

check:
	@command -v go >/dev/null || { echo 'ERROR: go is required' >&2; exit 1; }
	@command -v npm >/dev/null || { echo 'ERROR: npm is required' >&2; exit 1; }
	@command -v node >/dev/null || { echo 'ERROR: node is required' >&2; exit 1; }
	@command -v tar >/dev/null || { echo 'ERROR: tar is required' >&2; exit 1; }
	@command -v clang >/dev/null || { echo 'ERROR: clang is required' >&2; exit 1; }
	@command -v bpftool >/dev/null || { echo 'ERROR: bpftool is required' >&2; exit 1; }
	@echo "All required tools are installed."

fmt:
	@command -v gofmt >/dev/null || { echo 'fmt requires gofmt' >&2; exit 1; }
	@command -v clang-format >/dev/null || { echo 'fmt requires clang-format' >&2; exit 1; }
	gofmt -w "$(ROOT_DIR)"
	find "$(DAEMON_DIR)/nvml" "$(DAEMON_DIR)/ebpf" -type d -name build -prune -o \
		-type f \( -name '*.c' -o -name '*.h' \) -print0 \
		| xargs -0 -r clang-format -i
	@echo "Formatting complete."

build: release

release: check
	@echo "==> Building wedjat release ($(GOARCH))"
	@rm -rf "$(DIST_DIR)"
	@mkdir -p "$(DIST_DIR)"

	# 1. Build Daemon
	@$(MAKE) -C "$(DAEMON_DIR)" build-release GOARCH=$(GOARCH)

	# 2. Build UI (Web + TUI + Server integration)
	@$(MAKE) -C "$(UI_DIR)/web" build
	# @$(MAKE) -C "$(UI_DIR)/tui" build # (Uncomment when TUI is ready)

	# 3. Build CLI
	@echo "==> Building CLI"
	@CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build \
		-ldflags "-s -w -X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
		-X main.commit=$$(git rev-parse --short HEAD 2>/dev/null || echo none) \
		-X main.date=$$(date -u +%Y-%m-%d)" \
		-o "$(DIST_DIR)/wedjat" "$(ROOT_DIR)/cmd/wedjat"

	# 4. Package Release Archive
	@echo "==> Creating Release Archive"
	@rm -rf "$(STAGE_DIR)"
	@mkdir -p "$(STAGE_DIR)"

	@install -m 0755 "$(DIST_DIR)/wedjatd" "$(STAGE_DIR)/wedjatd"
	@install -m 0755 "$(DIST_DIR)/wedjat" "$(STAGE_DIR)/wedjat"
	@install -m 0644 "$(DIST_DIR)/config.yaml" "$(STAGE_DIR)/config.yaml"
	@install -m 0644 "$(DIST_DIR)/wedjatd.service" "$(STAGE_DIR)/wedjatd.service"
	@install -d -m 0755 "$(STAGE_DIR)/ebpf"
	@install -m 0644 "$(DIST_DIR)"/ebpf/*.bpf.o "$(STAGE_DIR)/ebpf/"

	@rm -f "$(ARCHIVE)" "$(ARCHIVE).sha256"
	@tar -C "$(STAGE_DIR)" -czf "$(ARCHIVE)" wedjat wedjatd config.yaml wedjatd.service ebpf
	@cd "$(DIST_DIR)" && sha256sum "$(notdir $(ARCHIVE))" > "$(notdir $(ARCHIVE)).sha256"
	@rm -rf "$(STAGE_DIR)"
	@echo "Created $(ARCHIVE)"
	@echo "Created $(ARCHIVE).sha256"

# CI target: the GitHub Actions workflow (ci.yml) runs `make ci`. It must
# succeed without sudo and without a GPU, so it builds the BPF objects, runs
# the Go test suite, and lints the Go and C sources — but never loads the
# programs into the kernel.
ci: check fmt
	@echo "==> CI: building BPF objects (no kernel load)"
	@$(MAKE) -C "$(DAEMON_DIR)" bpf
	@echo "==> CI: building development daemon"
	@$(MAKE) -C "$(DAEMON_DIR)" build-dev GOARCH=$(GOARCH)
	@echo "==> CI: running Go tests"
	@cd $(ROOT_DIR) && go test ./...
	@echo "==> CI: running daemon tests"
	@$(MAKE) -C "$(DAEMON_DIR)" test
	@echo "==> CI: running UI server tests"
	@cd $(ROOT_DIR) && go test ./ui/server/...
	@echo "CI passed."

# Lint target: format-check only (never rewrites files). Fails the build on
# drift instead of silently fixing it.
lint:
	@command -v gofmt >/dev/null || { echo 'lint requires gofmt' >&2; exit 1; }
	@command -v clang-format >/dev/null || { echo 'lint requires clang-format' >&2; exit 1; }
	@echo "==> gofmt check"
	@gofmt -l "$(ROOT_DIR)" | grep -v '^daemon/ebpf/build/' && exit 1 || true
	@echo "==> clang-format check"
	@find "$(DAEMON_DIR)/nvml" "$(DAEMON_DIR)/ebpf" -type d -name build -prune -o \
		-type f \( -name '*.c' -o -name '*.h' \) -print0 \
		| xargs -0 -r clang-format --dry-run --Werror
	@echo "Lint passed."

install:
	@[ -f "$(ARCHIVE)" ] || { echo "ERROR: missing $(ARCHIVE); run make release first" >&2; exit 1; }
	@[ -f "$(ARCHIVE).sha256" ] || { echo "ERROR: missing $(ARCHIVE).sha256; run make release first" >&2; exit 1; }
	sudo bash "$(ROOT_DIR)/scripts/install.sh" --local "$(DIST_DIR)"

uninstall:
	@if [ "$${EUID:-$$(id -u)}" -eq 0 ]; then \
		wedjat uninstall; \
	else \
		sudo wedjat uninstall; \
	fi

test:
	@echo "==> Running Daemon tests"
	@$(MAKE) -C "$(DAEMON_DIR)" test
	@echo "==> Running UI Server tests"
	@cd $(ROOT_DIR) && go test ./ui/server/...
	@echo "==> Running UI Web tests"
	@$(MAKE) -C "$(UI_DIR)/web" test
	@echo "All tests passed."

clean:
	@echo "==> Cleaning root artifacts"
	@rm -rf "$(DIST_DIR)"
	@echo "==> Cleaning daemon artifacts"
	@$(MAKE) -C "$(DAEMON_DIR)" clean
	@echo "==> Cleaning web UI artifacts"
	@$(MAKE) -C "$(UI_DIR)/web" clean
	@echo "Clean complete."

# Development targets
dev-daemon:
	@$(MAKE) -C "$(DAEMON_DIR)" run-dev

dev-ui:
	@echo "Starting dev UI (builds the web UI, then serves it embedded)..."
	@$(UI_DIR)/web/scripts/run.sh --dev

dev:
	@echo "Starting both daemon and UI in development mode..."
	@$(MAKE) -j2 dev-daemon dev-ui