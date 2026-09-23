SHELL := /bin/bash

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

ROOT          := ../../../..
KERNELS_DIR   := $(ROOT)/kernels_to_trace
KERNELS_BUILD := $(KERNELS_DIR)/build
BPF_SRC_DIR   := ..

# ---------------------------------------------------------------------------
# Tools — identical to uprobes/test/Makefile
# ---------------------------------------------------------------------------

CC      := clang
BPF_CC  := clang
BPFTOOL := bpftool

# ---------------------------------------------------------------------------
# Flags
# ---------------------------------------------------------------------------

BPF_CFLAGS := -g -O2 -target bpf -D__TARGET_ARCH_x86 \
              -I/usr/include/x86_64-linux-gnu

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

BIN := bin

# ---------------------------------------------------------------------------
# BPF program and CUDA inputs
#
# There is only one BPF program here: driver_kprobes.
# It is tested against k1 and k2 (both go through nvidia_unlocked_ioctl).
# ---------------------------------------------------------------------------

BPF_PROGRAM  := driver_kprobes
CUDA_PROGRAMS := k1 k2

PROGRAM ?= k2   # default fixture: Driver API (cuMemAlloc/cuStreamSynchronize)

.PHONY: help test test-all test-one list build build-bpf build-tests \
        build-inputs clean

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

help:
	@echo "Wedjat kprobe tests (C/libbpf)"
	@echo ""
	@echo "Prerequisites:"
	@echo "  clang, bpftool, libbpf-dev, linux-headers"
	@echo "  nvidia.ko and nvidia-uvm.ko loaded (for NVIDIA symbol probes)"
	@echo "  Root (CAP_BPF + CAP_PERFMON) to run tests."
	@echo ""
	@echo "Build targets:"
	@echo "  make build         Compile BPF object, generate skeleton, build test binary"
	@echo "  make build-bpf     BPF object + skeleton only"
	@echo "  make build-tests   C test binary only (needs skeleton)"
	@echo "  make build-inputs  Build CUDA fixture binaries in kernels_to_trace/"
	@echo ""
	@echo "Test targets (require root + GPU + nvidia.ko):"
	@echo "  make test              Run kprobe tests against all CUDA inputs"
	@echo "  make test-one PROGRAM=k2   Run against one CUDA input"
	@echo ""
	@echo "  make list          Show available CUDA inputs"
	@echo ""
	@echo "BPF program:   $(BPF_PROGRAM)"
	@echo "CUDA inputs:   $(CUDA_PROGRAMS)"

list:
	@echo "BPF_PROGRAM=$(BPF_PROGRAM)"
	@echo "CUDA_PROGRAMS=$(CUDA_PROGRAMS)"

# ---------------------------------------------------------------------------
# BPF compilation + skeleton generation
# ---------------------------------------------------------------------------

$(BIN):
	@mkdir -p $(BIN)

$(BIN)/$(BPF_PROGRAM).bpf.o: $(BPF_SRC_DIR)/$(BPF_PROGRAM).bpf.c | $(BIN)
	$(BPF_CC) $(BPF_CFLAGS) -c $< -o $@

$(BIN)/$(BPF_PROGRAM).skel.h: $(BIN)/$(BPF_PROGRAM).bpf.o
	$(BPFTOOL) gen skeleton $< > $@

build-bpf: $(BIN)/$(BPF_PROGRAM).bpf.o $(BIN)/$(BPF_PROGRAM).skel.h

# ---------------------------------------------------------------------------
# Test harness compilation
# ---------------------------------------------------------------------------

$(BIN)/$(BPF_PROGRAM)_test: $(BPF_PROGRAM)_test.c $(BIN)/$(BPF_PROGRAM).skel.h | $(BIN)
	$(CC) $(CFLAGS) -I$(BIN) -o $@ $< $(LIBS)

build-tests: $(BIN)/$(BPF_PROGRAM)_test

build: build-bpf build-tests

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

test-one: build build-inputs
	@test -n "$(PROGRAM)" || { echo "PROGRAM is required" >&2; exit 2; }
	@test -x "$(KERNELS_BUILD)/$(PROGRAM)" || { \
		echo "missing CUDA input: $(KERNELS_BUILD)/$(PROGRAM)" >&2; \
		echo "  run: make -C kernels_to_trace build" >&2; \
		exit 2; \
	}
	@echo "RUN  PROGRAM=$(PROGRAM)"
	sudo $(BIN)/$(BPF_PROGRAM)_test $(PROGRAM)
	@echo "PASS PROGRAM=$(PROGRAM)"

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean:
	$(RM) -r $(BIN)
