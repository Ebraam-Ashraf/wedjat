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

CC      := clang
BPF_CC  := clang
BPFTOOL := bpftool
ARCH    := $(shell uname -m | sed 's/x86_64/x86/;s/aarch64/arm64/')

# ---------------------------------------------------------------------------
# Build output
# ---------------------------------------------------------------------------

BUILD := build

# ---------------------------------------------------------------------------
# Flags
# ---------------------------------------------------------------------------

BPF_CFLAGS := -g -O2 -target bpf -D__TARGET_ARCH_$(ARCH) -D__bpf__ \
              -I$(BUILD) -I../..

HAVE_PKG_CONFIG := $(shell command -v pkg-config 2>/dev/null)
ifdef HAVE_PKG_CONFIG
    CFLAGS := -Wall -Wextra -O1 $(shell pkg-config --cflags libbpf 2>/dev/null)
    LIBS   := $(shell pkg-config --libs libbpf 2>/dev/null) -lelf -lz
else
    CFLAGS := -Wall -Wextra -O1
    LIBS   := -lbpf -lelf -lz
endif

.PHONY: all run clean

all: build-bpf build-tests build-inputs

# ---------------------------------------------------------------------------
# BPF compilation + skeleton generation
# ---------------------------------------------------------------------------

$(BUILD)/.d:
	@mkdir -p $(BUILD)
	@touch $@

$(BUILD)/vmlinux.h: | $(BUILD)/.d
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@

$(BUILD)/%.bpf.o: $(BPF_SRC_DIR)/%.bpf.c \
    $(BPF_SRC_DIR)/../common.h $(BUILD)/vmlinux.h | $(BUILD)/.d
	$(BPF_CC) $(BPF_CFLAGS) -c $< -o $@

$(BUILD)/%.skel.h: $(BUILD)/%.bpf.o
	$(BPFTOOL) gen skeleton $< > $@

build-bpf: $(BUILD)/cuda_actions.skel.h $(BUILD)/host_ctx.skel.h

# ---------------------------------------------------------------------------
# Test harness compilation
# ---------------------------------------------------------------------------

$(BUILD)/cuda_actions_test: cuda_actions_test.c \
    $(BPF_SRC_DIR)/../common.h \
    $(BUILD)/cuda_actions.skel.h $(BUILD)/host_ctx.skel.h | $(BUILD)/.d
	$(CC) $(CFLAGS) -I$(BUILD) -I../.. -o $@ $< $(LIBS)

$(BUILD)/host_ctx_test: host_ctx_test.c \
    $(BPF_SRC_DIR)/../common.h \
    $(BUILD)/host_ctx.skel.h | $(BUILD)/.d
	$(CC) $(CFLAGS) -I$(BUILD) -I../.. -o $@ $< $(LIBS)

build-tests: $(BUILD)/cuda_actions_test $(BUILD)/host_ctx_test

# ---------------------------------------------------------------------------
# CUDA fixture inputs
# ---------------------------------------------------------------------------

build-inputs:
	$(MAKE) -C $(KERNELS_DIR) build

# ---------------------------------------------------------------------------
# Test execution
# ---------------------------------------------------------------------------

# Usage:
#   make run                  # run all tests
#   make run TARGET=cuda_actions
#   make run TARGET=host_ctx

TARGET ?= all

run: all
ifeq ($(TARGET),all)
	sudo env LIBCUDA="$(LIBCUDA)" DUMP="$(DUMP)" $(BUILD)/cuda_actions_test $(KERNELS_BUILD)
	sudo env LIBCUDA="$(LIBCUDA)" DUMP="$(DUMP)" $(BUILD)/host_ctx_test     $(KERNELS_BUILD)
else
	sudo env LIBCUDA="$(LIBCUDA)" DUMP="$(DUMP)" $(BUILD)/$(TARGET)_test $(KERNELS_BUILD)
endif

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean:
	$(RM) -r $(BUILD)
	$(MAKE) -C $(KERNELS_DIR) clean
