SHELL := /bin/bash

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

ROOT          := ../../../..
KERNELS_DIR   := $(ROOT)/kernels_to_trace
KERNELS_BUILD := $(KERNELS_DIR)/build
BPF_SRC_DIR   := ..

# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------

# clang is required for both BPF bytecode compilation and the test harness.
# gcc cannot compile BPF programs — it does not emit BPF ELF.
CC      := clang
BPF_CC  := clang

# bpftool generates the skeleton header from a compiled .bpf.o.
# On Ubuntu/Debian: apt install linux-tools-$(uname -r)
# On Fedora/RHEL:   dnf install bpftool
BPFTOOL := bpftool

# ---------------------------------------------------------------------------
# Flags
# ---------------------------------------------------------------------------

# BPF compilation flags:
#   -g                   include BTF debug info (required for CO-RE)
#   -O2                  BPF verifier rejects unoptimized code in many cases
#   -target bpf          emit BPF ELF, not host ELF
#   -D__TARGET_ARCH_x86  tell bpf_tracing.h which register layout to use
#   -I/usr/include/...   find vmlinux.h and bpf/ headers
BPF_CFLAGS := -g -O2 -target bpf -D__TARGET_ARCH_x86 \
              -I/usr/include/x86_64-linux-gnu

# Test harness compilation flags.
# pkg-config provides the right -I and -L for whichever libbpf is installed.
# Fallback to hardcoded flags if pkg-config is absent.
HAVE_PKG_CONFIG := $(shell command -v pkg-config 2>/dev/null)
ifdef HAVE_PKG_CONFIG
    CFLAGS := -Wall -Wextra -O1 $(shell pkg-config --cflags libbpf 2>/dev/null)
    LIBS   := $(shell pkg-config --libs libbpf 2>/dev/null) -lelf -lz
else
    CFLAGS := -Wall -Wextra -O1
    LIBS   := -lbpf -lelf -lz
endif

# ---------------------------------------------------------------------------
# Build output
# ---------------------------------------------------------------------------

# All generated files land in bin/ so the source tree stays clean.
# bin/ is in .gitignore.
BIN := bin

# ---------------------------------------------------------------------------
# BPF programs and their matching test harnesses.
# Adding a new probe pair: add the name here, then create:
#   ../newprobe.bpf.c    — the BPF program
#   newprobe_test.c      — the C test harness (follow host_ctx_test.c shape)
BPF_PROGRAMS := host_ctx cuda_actions

# CUDA fixture programs that the tests use as trigger inputs.
# These must be built first: make -C kernels_to_trace build
CUDA_PROGRAMS := k1 k2

# Default test axis (overridable on the command line).
BPF     ?= host_ctx
PROGRAM ?= k1

.PHONY: help test test-all test-one test-host-ctx test-cuda-actions \
        list build build-bpf build-tests build-inputs clean help

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

help:
	@echo "Wedjat uprobe tests (C/libbpf)"
	@echo ""
	@echo "Prerequisites:"
	@echo "  clang, bpftool, libbpf-dev, linux-headers (for vmlinux.h)"
	@echo "  A real GPU and root (CAP_BPF + CAP_PERFMON) to run tests."
	@echo ""
	@echo "Build targets:"
	@echo "  make build            Compile BPF objects, generate skeletons,"
	@echo "                        and build C test binaries"
	@echo "  make build-bpf        BPF objects + skeletons only"
	@echo "  make build-tests      C test binaries only (needs skeletons)"
	@echo "  make build-inputs     Build CUDA fixture binaries in kernels_to_trace/"
	@echo ""
	@echo "Test targets (require root + GPU):"
	@echo "  make test             Run all probes against all CUDA inputs"
	@echo "  make test-host-ctx    Run host_ctx probe against all CUDA inputs"
	@echo "  make test-cuda-actions Run cuda_actions probe against all CUDA inputs"
	@echo "  make test-one BPF=host_ctx PROGRAM=k1"
	@echo "                        Run one probe against one CUDA input"
	@echo ""
	@echo "  make list             Show available probes and CUDA inputs"
	@echo ""
	@echo "BPF probes:    $(BPF_PROGRAMS)"
	@echo "CUDA inputs:   $(CUDA_PROGRAMS)"

list:
	@echo "BPF_PROGRAMS=$(BPF_PROGRAMS)"
	@echo "CUDA_PROGRAMS=$(CUDA_PROGRAMS)"

# ---------------------------------------------------------------------------
# BPF compilation + skeleton generation
#
# Step 1: compile the .bpf.c source to a BPF ELF (.bpf.o).
#         clang -target bpf produces bytecode the kernel verifier accepts.
#
# Step 2: bpftool gen skeleton reads the ELF and writes a .skel.h header.
#         The skeleton is a self-contained C header that contains:
#           - the raw BPF ELF embedded as a byte array
#           - typed accessors for every map and program
#           - open/load/attach/destroy functions
#         The test harness includes this header and calls these functions
#         instead of manually calling libbpf's lower-level API.
# ---------------------------------------------------------------------------

$(BIN):
	@mkdir -p $(BIN)

# Pattern rule: ../foo.bpf.c -> bin/foo.bpf.o -> bin/foo.skel.h
$(BIN)/%.bpf.o: $(BPF_SRC_DIR)/%.bpf.c | $(BIN)
	$(BPF_CC) $(BPF_CFLAGS) -c $< -o $@

$(BIN)/%.skel.h: $(BIN)/%.bpf.o
	$(BPFTOOL) gen skeleton $< > $@

build-bpf: $(BIN) $(patsubst %, $(BIN)/%.bpf.o, $(BPF_PROGRAMS)) \
                   $(patsubst %, $(BIN)/%.skel.h, $(BPF_PROGRAMS))

# ---------------------------------------------------------------------------
# Test harness compilation
#
# Each _test.c includes the skeleton header and links against libbpf.
# The skeleton header embeds the BPF ELF, so the resulting binary is
# self-contained — no separate .bpf.o file needed at runtime.
# ---------------------------------------------------------------------------

# host_ctx_test depends on host_ctx.skel.h
$(BIN)/host_ctx_test: host_ctx_test.c $(BIN)/host_ctx.skel.h | $(BIN)
	$(CC) $(CFLAGS) -I$(BIN) -o $@ $< $(LIBS)

# cuda_actions_test depends on cuda_actions.skel.h
$(BIN)/cuda_actions_test: cuda_actions_test.c $(BIN)/cuda_actions.skel.h | $(BIN)
	$(CC) $(CFLAGS) -I$(BIN) -o $@ $< $(LIBS)

build-tests: $(patsubst %, $(BIN)/%_test, $(BPF_PROGRAMS))

build: build-bpf build-tests

# ---------------------------------------------------------------------------
# CUDA fixture inputs
# ---------------------------------------------------------------------------

build-inputs:
	$(MAKE) -C $(KERNELS_DIR) build

# ---------------------------------------------------------------------------
# Test execution
#
# Tests need root because uprobe attachment requires CAP_BPF + CAP_PERFMON.
# sudo is added here so "make test" works from a normal user session.
# ---------------------------------------------------------------------------

test: test-all

test-all: build build-inputs
	@set -e; \
	for bpf in $(BPF_PROGRAMS); do \
		for program in $(CUDA_PROGRAMS); do \
			$(MAKE) test-one BPF=$$bpf PROGRAM=$$program; \
		done; \
	done

test-host-ctx: build build-inputs
	@set -e; \
	for program in $(CUDA_PROGRAMS); do \
		$(MAKE) test-one BPF=host_ctx PROGRAM=$$program; \
	done

test-cuda-actions: build build-inputs
	@set -e; \
	for program in $(CUDA_PROGRAMS); do \
		$(MAKE) test-one BPF=cuda_actions PROGRAM=$$program; \
	done

test-one: build build-inputs
	@test -n "$(BPF)"     || { echo "BPF is required"     >&2; exit 2; }
	@test -n "$(PROGRAM)" || { echo "PROGRAM is required"  >&2; exit 2; }
	@test -x "$(KERNELS_BUILD)/$(PROGRAM)" || { \
		echo "missing CUDA input: $(KERNELS_BUILD)/$(PROGRAM)" >&2; \
		echo "  run: make -C kernels_to_trace build" >&2; \
		exit 2; \
	}
	@test -x "$(BIN)/$(BPF)_test" || { \
		echo "missing test binary: $(BIN)/$(BPF)_test" >&2; \
		echo "  run: make build" >&2; \
		exit 2; \
	}
	@echo "RUN  BPF=$(BPF) PROGRAM=$(PROGRAM)"
	sudo $(BIN)/$(BPF)_test $(PROGRAM)
	@echo "PASS BPF=$(BPF) PROGRAM=$(PROGRAM)"

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean:
	$(RM) -r $(BIN)
