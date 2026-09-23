SHELL := /bin/bash

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

ROOT          := ../../../..
KERNELS_DIR   := $(ROOT)/kernels_to_trace
KERNELS_BUILD := $(KERNELS_DIR)/build

# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------

CC := clang

# ---------------------------------------------------------------------------
# CUDA / CUPTI paths
#
# Adjust CUDA_HOME if your CUDA Toolkit is installed elsewhere.
# Default: /usr/local/cuda (standard on Ubuntu/Debian with runfile install).
# On systems using package manager installs it may be /usr/cuda or similar.
# ---------------------------------------------------------------------------

CUDA_HOME ?= /usr/local/cuda

CUDA_INCLUDE := $(CUDA_HOME)/include
CUDA_LIB     := $(CUDA_HOME)/lib64

# ---------------------------------------------------------------------------
# Flags
#
# Two build modes:
#
#   stub (default, no CUDA/CUPTI required):
#     Compiles the test binary with just libc.
#     fork/exec/waitpid skeleton is real and runnable.
#     CUPTI and cuDeviceGetAttribute blocks are in TODO comments.
#     Use this to verify the harness compiles in CI without a GPU.
#
#   cupti (requires CUDA Toolkit):
#     Links against libcupti and libcuda.
#     Uncomment real CUPTI blocks in cupti_sm_test.c before using this mode.
#     Build with: make build-cupti
# ---------------------------------------------------------------------------

CFLAGS_STUB  := -Wall -Wextra -O1
CFLAGS_CUPTI := -Wall -Wextra -O1 -I$(CUDA_INCLUDE)
LIBS_CUPTI   := -L$(CUDA_LIB) -lcupti -lcuda -Wl,-rpath,$(CUDA_LIB)

BIN := bin

CUDA_PROGRAMS := k1 k2 k8

PROGRAM ?= k1   # default fixture: Runtime API (calls cuLaunchKernel visibly)

.PHONY: help build build-stub build-cupti build-inputs test test-all \
        test-one list clean

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

help:
	@echo "Wedjat Layer 5 tests (CUPTI)"
	@echo ""
	@echo "NOTE: This directory does NOT contain BPF tests."
	@echo "      Layer 5 uses CUPTI, not eBPF. See device/README.md."
	@echo ""
	@echo "Build targets:"
	@echo "  make build          Build stub binary (no CUDA/CUPTI required)"
	@echo "  make build-cupti    Build with real CUPTI (requires CUDA Toolkit)"
	@echo "  make build-inputs   Build CUDA fixture binaries"
	@echo ""
	@echo "Test targets (require root + GPU):"
	@echo "  make test           Run against all CUDA inputs"
	@echo "  make test-one PROGRAM=k1   Run against one CUDA input"
	@echo ""
	@echo "  make list           Show available CUDA inputs"
	@echo ""
	@echo "CUDA inputs:   $(CUDA_PROGRAMS)"
	@echo "CUDA_HOME:     $(CUDA_HOME)  (override if needed)"

list:
	@echo "CUDA_PROGRAMS=$(CUDA_PROGRAMS)"
	@echo "CUDA_HOME=$(CUDA_HOME)"

# ---------------------------------------------------------------------------
# Build targets
# ---------------------------------------------------------------------------

$(BIN):
	@mkdir -p $(BIN)

# Stub build: no CUDA/CUPTI dependency.
# The fork/exec/waitpid skeleton compiles and runs without a GPU.
$(BIN)/cupti_sm_test_stub: cupti_sm_test.c | $(BIN)
	$(CC) $(CFLAGS_STUB) -o $@ $<

build-stub: $(BIN)/cupti_sm_test_stub

# CUPTI build: real integration, requires CUDA Toolkit.
# Uncomment CUPTI blocks in cupti_sm_test.c before using this.
$(BIN)/cupti_sm_test: cupti_sm_test.c | $(BIN)
	$(CC) $(CFLAGS_CUPTI) -o $@ $< $(LIBS_CUPTI)

build-cupti: $(BIN)/cupti_sm_test

# Default build is stub so CI works without a GPU.
build: build-stub

# ---------------------------------------------------------------------------
# CUDA fixture inputs
# ---------------------------------------------------------------------------

build-inputs:
	$(MAKE) -C $(KERNELS_DIR) build

# ---------------------------------------------------------------------------
# Test execution
# ---------------------------------------------------------------------------

test: test-all

test-all: build build-inputs
	@set -e; \
	for program in $(CUDA_PROGRAMS); do \
		$(MAKE) test-one PROGRAM=$$program; \
	done

# test-one runs the stub by default (no GPU required).
# To run the real CUPTI test: make test-one PROGRAM=k1 USE_CUPTI=1
USE_CUPTI ?= 0

test-one: build build-inputs
	@test -n "$(PROGRAM)" || { echo "PROGRAM is required" >&2; exit 2; }
	@test -x "$(KERNELS_BUILD)/$(PROGRAM)" || { \
		echo "missing CUDA input: $(KERNELS_BUILD)/$(PROGRAM)" >&2; \
		echo "  run: make -C kernels_to_trace build" >&2; \
		exit 2; \
	}
ifeq ($(USE_CUPTI),1)
	@test -x "$(BIN)/cupti_sm_test" || { \
		echo "run: make build-cupti first" >&2; exit 2; }
	@echo "RUN  CUPTI PROGRAM=$(PROGRAM)"
	sudo $(BIN)/cupti_sm_test $(PROGRAM)
else
	@echo "RUN  STUB  PROGRAM=$(PROGRAM)"
	$(BIN)/cupti_sm_test_stub $(PROGRAM)
endif
	@echo "PASS PROGRAM=$(PROGRAM)"

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean:
	$(RM) -r $(BIN)
