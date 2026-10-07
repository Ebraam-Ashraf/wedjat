SHELL := /bin/bash

DAEMON_DIR := $(CURDIR)
REPO_DIR   := $(abspath $(CURDIR)/..)

GOARCH ?= amd64
DIST_DIR := $(REPO_DIR)/dist
DEV_BUILD_DIR := $(DIST_DIR)/dev

GO_ENV := CGO_ENABLED=1 GOOS=linux GOARCH=$(GOARCH)

GO_CANDIDATES := /usr/local/go/bin/go /usr/lib/go/bin/go
GO := $(firstword $(wildcard $(GO_CANDIDATES)) go)

.PHONY: all help build-dev run-dev build-release test clean bpf kernels

all: build-dev

help:
	@echo "Wedjat Daemon — Makefile"
	@echo "  make               Build development daemon"
	@echo "  make run-dev       Build and run dev daemon (needs sudo for eBPF)"
	@echo "  make build-release Build production daemon into dist/"
	@echo "  make test          Run all daemon tests"
	@echo "  make clean         Delete build artifacts"

bpf:
	@cd $(REPO_DIR) && ./daemon/ebpf/build.sh

kernels:
	@$(MAKE) -C $(REPO_DIR)/kernels_to_trace build

build-dev: bpf
	@echo "==> Building development daemon"
	@mkdir -p $(DEV_BUILD_DIR)
	@cd $(REPO_DIR) && $(GO_ENV) $(GO) build -o $(DEV_BUILD_DIR)/wedjatd-dev ./daemon/cmd/wedjatd-dev
	@echo "Built: $(DEV_BUILD_DIR)/wedjatd-dev"

run-dev: build-dev
	@echo "==> Running development daemon"
	@echo "    (Press Ctrl+C to stop)"
	@sudo $(DEV_BUILD_DIR)/wedjatd-dev

build-release: bpf
	@test -f $(DAEMON_DIR)/cmd/wedjatd/main.go || { echo "release blocked: daemon/cmd/wedjatd/main.go does not exist" >&2; exit 2; }
	@echo "==> Building production daemon"
	@mkdir -p "$(DIST_DIR)"
	@cd $(REPO_DIR) && $(GO_ENV) $(GO) build -trimpath -ldflags='-s -w' -o "$(DIST_DIR)/wedjatd" ./daemon/cmd/wedjatd
	@install -m 0644 $(DAEMON_DIR)/packaging/config.yaml "$(DIST_DIR)/config.yaml"
	@install -m 0644 $(DAEMON_DIR)/packaging/wedjatd.service "$(DIST_DIR)/wedjatd.service"
	@rm -rf "$(DIST_DIR)/ebpf"
	@install -d -m 0755 "$(DIST_DIR)/ebpf"
	@install -m 0644 $(DAEMON_DIR)/ebpf/build/*.bpf.o "$(DIST_DIR)/ebpf/"
	@echo "Built: $(DIST_DIR)/wedjatd"

test: build-dev
	@echo "==> Running Go tests"
	@cd $(REPO_DIR) && $(GO_ENV) $(GO) test ./daemon/...
	@echo ""
	@echo "==> NVML poller and XID tests"
	@$(MAKE) -C $(DAEMON_DIR)/nvml/test test
	@echo ""
	@echo "==> eBPF verifier load"
	@sudo $(MAKE) -C $(DAEMON_DIR)/ebpf/proc/test verify

clean:
	@echo "==> Cleaning daemon build artifacts"
	@rm -rf $(DEV_BUILD_DIR)/wedjatd-dev
	@rm -f $(DIST_DIR)/wedjatd $(DIST_DIR)/config.yaml $(DIST_DIR)/wedjatd.service
	@rm -rf $(DIST_DIR)/ebpf
	@echo "==> Removing eBPF objects"
	@rm -rf $(DAEMON_DIR)/ebpf/build
	@echo "==> Removing test binaries"
	@rm -rf $(DAEMON_DIR)/nvml/test/build $(DAEMON_DIR)/ebpf/proc/test/build $(DAEMON_DIR)/ebpf/uprobes/test/build $(DAEMON_DIR)/ebpf/kprobes/test/build $(DAEMON_DIR)/ebpf/gpu/test/build
	@echo "==> Removing CUDA fixtures"
	@-$(MAKE) -C $(REPO_DIR)/kernels_to_trace clean 2>/dev/null || true
	@echo "==> Clean."
