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

.PHONY: all test clean

all: build-bpf build-tests build-inputs

# ---------------------------------------------------------------------------
# BPF compilation + skeleton generation
# ---------------------------------------------------------------------------

$(BUILD)/.d:
	@mkdir -p $(BUILD)
	@touch $@

$(BUILD)/vmlinux.h: | $(BUILD)/.d
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@

$(BUILD)/driver_kprobes.bpf.o: $(BPF_SRC_DIR)/driver_kprobes.bpf.c \
    $(BPF_SRC_DIR)/../common.h $(BUILD)/vmlinux.h | $(BUILD)/.d
	$(BPF_CC) $(BPF_CFLAGS) -c $< -o $@

$(BUILD)/driver_kprobes.skel.h: $(BUILD)/driver_kprobes.bpf.o
	$(BPFTOOL) gen skeleton $< > $@

$(BUILD)/proc_lifecycle.bpf.o: ../../proc_lifecycle.bpf.c ../../common.h $(BUILD)/vmlinux.h | $(BUILD)/.d
	$(BPF_CC) $(BPF_CFLAGS) -c $< -o $@

$(BUILD)/proc_lifecycle.skel.h: $(BUILD)/proc_lifecycle.bpf.o
	$(BPFTOOL) gen skeleton $< > $@

build-bpf: $(BUILD)/driver_kprobes.skel.h $(BUILD)/proc_lifecycle.skel.h

# ---------------------------------------------------------------------------
# Test harness compilation
# ---------------------------------------------------------------------------

$(BUILD)/driver_kprobes_test: driver_kprobes_test.c \
    $(BPF_SRC_DIR)/../common.h $(BUILD)/driver_kprobes.skel.h | $(BUILD)/.d
	$(CC) $(CFLAGS) -I$(BUILD) -I../.. -o $@ $< $(LIBS)

$(BUILD)/proc_lifecycle_test: proc_lifecycle_test.c $(BPF_SRC_DIR)/../common.h $(BUILD)/proc_lifecycle.skel.h | $(BUILD)/.d
	$(CC) $(CFLAGS) -I$(BUILD) -I../.. -pthread -o $@ $< $(LIBS)

build-tests: $(BUILD)/driver_kprobes_test $(BUILD)/proc_lifecycle_test

# ---------------------------------------------------------------------------
# CUDA fixture inputs
# ---------------------------------------------------------------------------

build-inputs:
	$(MAKE) -C $(KERNELS_DIR) build

# ---------------------------------------------------------------------------
# Test execution
# ---------------------------------------------------------------------------

# Usage:
#   make run                        # run driver_kprobes_test
#   make run TARGET=driver_kprobes  # same thing (explicit)

TARGET ?= driver_kprobes

check-modules:
	@if ! grep -q ' t uvm_\| T uvm_' /proc/kallsyms 2>/dev/null; then \
		echo "==> nvidia-uvm.ko not loaded, loading..."; \
		sudo modprobe nvidia-uvm || { echo "ERROR: modprobe nvidia-uvm failed"; exit 1; }; \
	else \
		echo "==> nvidia-uvm.ko already loaded"; \
	fi

ifeq ($(TARGET),proc_lifecycle)
test: build-bpf build-tests
	$(BUILD)/proc_lifecycle_test
else
test: all check-modules
	sudo env DUMP="$(DUMP)" $(BUILD)/$(TARGET)_test $(KERNELS_BUILD)
endif

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean:
	$(RM) -r $(BUILD)
	$(MAKE) -C $(KERNELS_DIR) clean
