SHELL := /bin/bash

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

ROOT          := ../../..
KERNELS_DIR   := $(ROOT)/kernels_to_trace
KERNELS_BUILD := $(KERNELS_DIR)/build
NVML_SRC_DIR  := ..

# ---------------------------------------------------------------------------
# Tools and flags
#
# NVML does not need clang — plain gcc or clang both work.
# The test files #include ../poller.c and ../xid.c directly (not via headers)
# so a single compilation unit per test is sufficient while the source files
# are comment-only stubs with no external dependencies.
#
# When poller.c/xid.c have real NVML calls, link with -lnvidia-ml.
# ---------------------------------------------------------------------------

CC     := gcc
CFLAGS := -Wall -Wextra -O1

# CUDA Toolkit include path for nvml.h.
# Adjust CUDA_HOME if your Toolkit is not at /usr/local/cuda.
CUDA_HOME ?= /usr/local/cuda
NVML_CFLAGS := -I$(CUDA_HOME)/include

# libnvidia-ml is usually in the driver lib path, not the CUDA Toolkit.
# On Ubuntu it is at /usr/lib/x86_64-linux-gnu/libnvidia-ml.so.1 (via the
# driver package).  Adjust NVML_LIB_DIR if it is elsewhere.
NVML_LIB_DIR ?= /usr/lib/x86_64-linux-gnu
NVML_LIBS    := -L$(NVML_LIB_DIR) -lnvidia-ml

# ---------------------------------------------------------------------------
# Two build modes:
#
#   stub (default):
#     No nvml.h / libnvidia-ml needed.  Compiles the harness skeleton.
#     poller.c / xid.c TODO blocks are not uncommented.
#     Run without a GPU to verify the harness compiles.
#
#   nvml:
#     Links against libnvidia-ml with the real NVML header.
#     Use after uncommenting the TODO blocks in poller.c and xid.c.
#     Build with: make build-nvml
# ---------------------------------------------------------------------------

BIN := bin

CUDA_PROGRAMS := k1 k2

PROGRAM ?= k1

.PHONY: help build build-stub build-nvml build-inputs \
        test run-poller-test run-xid-test run-xid-test-manual \
        list clean

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

help:
	@echo "Wedjat NVML tests"
	@echo ""
	@echo "NOTE: No BPF skeleton here. These tests link against libnvidia-ml."
	@echo ""
	@echo "Build targets:"
	@echo "  make build          Build stub binaries (no NVML required)"
	@echo "  make build-nvml     Build with real libnvidia-ml"
	@echo "  make build-inputs   Build CUDA fixture binaries"
	@echo ""
	@echo "Test targets (require GPU + nvidia-smi working):"
	@echo "  make test                  Run automated tests (poller + xid plumbing)"
	@echo "  make run-poller-test       poller.c: global stats + per-process pid check"
	@echo "  make run-xid-test          xid.c: event plumbing, timeout = pass"
	@echo "  make run-xid-test-manual   xid.c: blocks for real fault — NOT for CI"
	@echo ""
	@echo "  make list           Show available CUDA inputs"
	@echo ""
	@echo "CUDA inputs:  $(CUDA_PROGRAMS)"
	@echo "CUDA_HOME:    $(CUDA_HOME)   (override if needed)"
	@echo "NVML_LIB_DIR: $(NVML_LIB_DIR)   (override if needed)"

list:
	@echo "CUDA_PROGRAMS=$(CUDA_PROGRAMS)"

# ---------------------------------------------------------------------------
# Build targets
# ---------------------------------------------------------------------------

$(BIN):
	@mkdir -p $(BIN)

# Stub builds — no NVML header/lib needed.
$(BIN)/poller_test_stub: poller_test.c $(NVML_SRC_DIR)/poller.c | $(BIN)
	$(CC) $(CFLAGS) -o $@ $<

$(BIN)/xid_test_stub: xid_test.c $(NVML_SRC_DIR)/xid.c | $(BIN)
	$(CC) $(CFLAGS) -o $@ $<

build-stub: $(BIN)/poller_test_stub $(BIN)/xid_test_stub

# NVML builds — requires CUDA Toolkit + libnvidia-ml.
$(BIN)/poller_test: poller_test.c $(NVML_SRC_DIR)/poller.c | $(BIN)
	$(CC) $(CFLAGS) $(NVML_CFLAGS) -o $@ $< $(NVML_LIBS)

$(BIN)/xid_test: xid_test.c $(NVML_SRC_DIR)/xid.c | $(BIN)
	$(CC) $(CFLAGS) $(NVML_CFLAGS) -o $@ $< $(NVML_LIBS)

build-nvml: $(BIN)/poller_test $(BIN)/xid_test

# Default build is stub so CI works without NVML.
build: build-stub

# ---------------------------------------------------------------------------
# CUDA fixture inputs
# ---------------------------------------------------------------------------

build-inputs:
	$(MAKE) -C $(KERNELS_DIR) build

# ---------------------------------------------------------------------------
# Test execution
#
# USE_NVML=1 runs the real NVML binaries instead of stubs.
# ---------------------------------------------------------------------------

USE_NVML ?= 0

run-poller-test: build build-inputs
	@test -x "$(KERNELS_BUILD)/$(PROGRAM)" || { \
		echo "missing fixture: $(KERNELS_BUILD)/$(PROGRAM)" >&2; \
		echo "  run: make -C kernels_to_trace build" >&2; \
		exit 2; \
	}
ifeq ($(USE_NVML),1)
	@test -x "$(BIN)/poller_test" || { echo "run: make build-nvml first" >&2; exit 2; }
	@echo "RUN  poller_test PROGRAM=$(PROGRAM)"
	$(BIN)/poller_test $(PROGRAM)
else
	@echo "RUN  poller_test (stub) PROGRAM=$(PROGRAM)"
	$(BIN)/poller_test_stub $(PROGRAM)
endif
	@echo "PASS poller_test"

run-xid-test: build
ifeq ($(USE_NVML),1)
	@test -x "$(BIN)/xid_test" || { echo "run: make build-nvml first" >&2; exit 2; }
	@echo "RUN  xid_test"
	$(BIN)/xid_test
else
	@echo "RUN  xid_test (stub)"
	$(BIN)/xid_test_stub
endif
	@echo "PASS xid_test"

# Manual mode — blocked waiting for a real hardware fault.
# NOT included in the 'test' target — do not run in CI.
run-xid-test-manual: build
	@echo ""
	@echo "MANUAL TEST — this blocks until a real Xid fault fires."
	@echo "  Run a GPU stress test in another terminal, or wait."
	@echo "  Watch:  dmesg -w | grep -i xid"
	@echo "  Ctrl-C to abort."
	@echo ""
ifeq ($(USE_NVML),1)
	$(BIN)/xid_test --manual
else
	$(BIN)/xid_test_stub --manual
endif

# Automated test target — runs poller + xid plumbing only.
# Does NOT include run-xid-test-manual.
test: run-poller-test run-xid-test
	@echo "all NVML automated tests passed"
	@echo "(xid coverage is plumbing-only — see README for manual fault test)"

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean:
	$(RM) -r $(BIN)
