SHELL := /bin/bash

DAEMON_DIR := $(CURDIR)
REPO_DIR   := $(abspath $(CURDIR)/..)

GOARCH ?= amd64
DIST_DIR := $(DAEMON_DIR)/dist
DEV_BUILD_DIR := $(DAEMON_DIR)/dev/build
RELEASE_NAME ?= wedjat-linux-$(GOARCH)

GO_ENV := CGO_ENABLED=1 GOOS=linux GOARCH=$(GOARCH)

GO_CANDIDATES := /usr/local/go/bin/go /usr/lib/go/bin/go
GO := $(firstword $(wildcard $(GO_CANDIDATES)) go)

.PHONY: all help dev build-dev release test clean bpf kernels

all: build-dev

help:
	@echo "Wedjat Daemon - Makefile"
	@echo "  make               Build development daemon (no run)"
	@echo "  make dev           Build and run development daemon"
	@echo "  make test          Build and run all unit tests"
	@echo "  make release       Build production release into dist/"
	@echo "  make clean         Delete all build artifacts"

bpf:
	@cd $(REPO_DIR) && ./scripts/build_bpf.sh

kernels:
	@$(MAKE) -C $(REPO_DIR)/kernels_to_trace build

build-dev: bpf
	@echo "==> Building development daemon"
	@mkdir -p $(DEV_BUILD_DIR)
	@cd $(DAEMON_DIR) && $(GO_ENV) $(GO) build -o $(DEV_BUILD_DIR)/wedjatd-dev ./cmd/wedjatd-dev
	@echo "Built: $(DEV_BUILD_DIR)/wedjatd-dev"

dev: build-dev
	@echo "==> Running development daemon"
	@echo "    (Press Ctrl+C to stop)"
	@sudo $(DEV_BUILD_DIR)/wedjatd-dev

release: bpf
	@test -f cmd/wedjatd/main.go || { echo "release blocked: cmd/wedjatd/main.go does not exist" >&2; exit 2; }
	@echo "==> Building production release"
	@mkdir -p "$(DIST_DIR)"
	@cd $(DAEMON_DIR) && $(GO_ENV) $(GO) build -trimpath -ldflags='-s -w' -o "$(DIST_DIR)/wedjatd" ./cmd/wedjatd
	@install -m 0755 $(REPO_DIR)/scripts/uninstall.sh "$(DIST_DIR)/wedjat-uninstall"
	@install -m 0644 $(REPO_DIR)/scripts/config.yaml "$(DIST_DIR)/config.yaml"
	@install -m 0644 $(REPO_DIR)/scripts/wedjatd.service "$(DIST_DIR)/wedjatd.service"
	@rm -rf "$(DIST_DIR)/ebpf"
	@install -d -m 0755 "$(DIST_DIR)/ebpf"
	@install -m 0644 ebpf/build/*.bpf.o "$(DIST_DIR)/ebpf/"
	@cd "$(DIST_DIR)" && tar -czf "$(RELEASE_NAME).tar.gz" wedjatd wedjat-uninstall config.yaml wedjatd.service ebpf
	@cd "$(DIST_DIR)" && sha256sum "$(RELEASE_NAME).tar.gz" > "$(RELEASE_NAME).tar.gz.sha256"
	@echo "Created $(DIST_DIR)/$(RELEASE_NAME).tar.gz"

test: build-dev
	@echo "==> Running Go tests"
	@$(GO_ENV) $(GO) test ./...
	@echo ""
	@echo "==> NVML poller and XID tests"
	@$(MAKE) -C nvml/test test
	@echo ""
	@echo "==> eBPF verifier load"
	@sudo $(MAKE) -C ebpf/proc/test verify

clean:
	@echo "==> Removing build artifacts (needs sudo for dev/)"
	@if [ -d dev ]; then sudo rm -rf dev; fi
	@rm -rf dist
	@echo "==> Removing eBPF objects"
	@rm -rf ebpf/build
	@echo "==> Removing test binaries"
	@rm -rf nvml/test/build ebpf/proc/test/build ebpf/uprobes/test/build ebpf/kprobes/test/build ebpf/gpu/test/build
	@echo "==> Removing CUDA fixtures"
	@-$(MAKE) -C $(REPO_DIR)/kernels_to_trace clean
	@echo "==> Clean."
