SHELL := /bin/bash

# proc/test — compile and verifier-load proc_lifecycle.bpf.o
# ROOT  points to daemon/ebpf/proc (parent of this directory)
# EBPF  points to daemon/ebpf      (common.h lives here)
ROOT   := ..
EBPF   := ../..
BUILD  := build
ARCH   := $(shell uname -m | sed 's/x86_64/x86/;s/aarch64/arm64/')

BPF_CFLAGS := -g -O2 -target bpf -D__TARGET_ARCH_$(ARCH) -D__bpf__ \
              -I$(BUILD) -I$(EBPF)

HAVE_PKG_CONFIG := $(shell command -v pkg-config 2>/dev/null)
ifdef HAVE_PKG_CONFIG
    CFLAGS := -Wall -Wextra -O2 $(shell pkg-config --cflags libbpf 2>/dev/null)
    LIBS   := $(shell pkg-config --libs libbpf 2>/dev/null) -lelf -lz
else
    CFLAGS := -Wall -Wextra -O2
    LIBS   := -lbpf -lelf -lz
endif

.PHONY: all build verify test clean

all: build

$(BUILD)/.d:
	@mkdir -p $(BUILD)
	@touch $@

$(BUILD)/vmlinux.h: | $(BUILD)/.d
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@

$(BUILD)/proc_lifecycle.bpf.o: $(ROOT)/proc_lifecycle.bpf.c $(EBPF)/common.h $(BUILD)/vmlinux.h | $(BUILD)/.d
	clang $(BPF_CFLAGS) -c $< -o $@

$(BUILD)/load_host_objects: $(ROOT)/load_host_objects.c | $(BUILD)/.d
	$(CC) $(CFLAGS) -o $@ $< $(LIBS)

build: $(BUILD)/proc_lifecycle.bpf.o $(BUILD)/load_host_objects

verify: build
	$(BUILD)/load_host_objects $(BUILD)/proc_lifecycle.bpf.o

test: verify

clean:
	$(RM) -r $(BUILD)
