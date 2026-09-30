SHELL := /bin/bash

C_SOURCES := $(shell find daemon -type f \( -name '*.c' -o -name '*.h' \) | sort)

GOARCH ?= amd64
DIST_DIR ?= $(PWD)/dist
RELEASE_NAME ?= wedjat-linux-$(GOARCH)

.PHONY: help check check-whitespace format-check docs-check ci kernels kernels-smoke kernels-clean bpf dev release install clean

help:
	@echo "Wedjat development targets"
	@echo "  make check            Run all cheap local/CI checks"
	@echo "  make format-check     Check C/eBPF formatting if clang-format exists"
	@echo "  make check-whitespace Check source/config files for trailing whitespace"
	@echo "  make docs-check       Check important development docs exist"
	@echo "  make kernels          Build CUDA trace fixtures"
	@echo "  make kernels-smoke    Build and run small CUDA fixture smoke test"

check: format-check check-whitespace docs-check

ci: check

format-check:
	@if command -v clang-format >/dev/null 2>&1; then \
		clang-format --dry-run --Werror $(C_SOURCES); \
	else \
		echo "clang-format not installed; skipping format-check"; \
	fi

check-whitespace:
	@! git grep -n '[[:blank:]]$$' -- \
		'*.c' '*.h' '*.cu' '.clang-format' '.editorconfig' 'Makefile'

docs-check:
	@test -f docs/development.md
	@test -f kernels_to_trace/README.md
	@test -f daemon/ebpf/uprobes/test/README.md
	@test -f daemon/ebpf/kprobes/test/README.md
	@test -f daemon/ebpf/gpu/README.md
	@test -f daemon/nvml/test/README.md

kernels:
	$(MAKE) -C kernels_to_trace build

kernels-smoke:
	$(MAKE) -C kernels_to_trace smoke

kernels-clean:
	$(MAKE) -C kernels_to_trace clean

bpf:
	./scripts/build_bpf.sh

dev: bpf
	@test -f daemon/cmd/wedjatd/main.go || { echo "dev blocked: daemon/cmd/wedjatd/main.go does not exist yet" >&2; exit 2; }
	@mkdir -p "$(DIST_DIR)"
	@cd daemon && CGO_ENABLED=1 GOOS=linux GOARCH=$(GOARCH) go build -o "$(DIST_DIR)/wedjatd" ./cmd/wedjatd
	@echo "==> Running wedjatd in local dev mode..."
	@sudo "$(DIST_DIR)/wedjatd" --dev

release: bpf
	@test -f daemon/cmd/wedjatd/main.go || { echo "release blocked: daemon/cmd/wedjatd/main.go does not exist yet" >&2; exit 2; }
	@mkdir -p "$(DIST_DIR)"
	@cd daemon && CGO_ENABLED=1 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -ldflags='-s -w' -o "$(DIST_DIR)/wedjatd" ./cmd/wedjatd
	@if [ -d daemon/cmd/wedjat ]; then cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -ldflags='-s -w' -o "$(DIST_DIR)/wedjat" ./cmd/wedjat; fi
	@install -m 0755 scripts/uninstall.sh "$(DIST_DIR)/wedjat-uninstall"
	@install -m 0644 scripts/config.yaml "$(DIST_DIR)/config.yaml"
	@install -m 0644 scripts/wedjatd.service "$(DIST_DIR)/wedjatd.service"
	@cd "$(DIST_DIR)" && tar -czf "$(RELEASE_NAME).tar.gz" wedjatd wedjat-uninstall config.yaml wedjatd.service $$(test ! -f wedjat || printf '%s' wedjat)
	@cd "$(DIST_DIR)" && sha256sum "$(RELEASE_NAME).tar.gz" > "$(RELEASE_NAME).tar.gz.sha256"
	@echo "Created $(DIST_DIR)/$(RELEASE_NAME).tar.gz"

install: release
	sudo ./scripts/install.sh --local "$(DIST_DIR)"

clean:
	rm -f dist/wedjat-linux-*.tar.gz dist/wedjat-linux-*.tar.gz.sha256 dist/wedjatd dist/wedjat dist/wedjat-uninstall dist/config.yaml dist/wedjatd.service
	-rmdir dist 2>/dev/null
