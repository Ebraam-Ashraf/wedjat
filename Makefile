SHELL := /bin/bash

C_SOURCES := $(shell find daemon -type f \( -name '*.c' -o -name '*.h' \) | sort)

.PHONY: help check check-whitespace format-check docs-check ci kernels kernels-smoke kernels-clean

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
