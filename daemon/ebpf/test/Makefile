SHELL := /bin/bash
ROOT := ..
BUILD := build/verify
ARCH := $(shell uname -m | sed 's/x86_64/x86/;s/aarch64/arm64/')
BPF_CFLAGS := -g -O2 -target bpf -D__TARGET_ARCH_$(ARCH) -D__bpf__ -I$(BUILD) -I..
HOST_OBJECTS := $(BUILD)/cuda_actions.bpf.o $(BUILD)/host_ctx.bpf.o \
                $(BUILD)/driver_kprobes.bpf.o $(BUILD)/proc_lifecycle.bpf.o
HAVE_PKG_CONFIG := $(shell command -v pkg-config 2>/dev/null)
ifdef HAVE_PKG_CONFIG
CFLAGS := -Wall -Wextra -O2 $(shell pkg-config --cflags libbpf 2>/dev/null)
LIBS := $(shell pkg-config --libs libbpf 2>/dev/null) -lelf -lz
else
CFLAGS := -Wall -Wextra -O2
LIBS := -lbpf -lelf -lz
endif

.PHONY: all build verify clean
all: build

$(BUILD):
	mkdir -p $@

$(BUILD)/vmlinux.h: | $(BUILD)
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@

$(BUILD)/%.bpf.o: $(ROOT)/uprobes/%.bpf.c $(ROOT)/common.h $(BUILD)/vmlinux.h
	clang $(BPF_CFLAGS) -c $< -o $@

$(BUILD)/driver_kprobes.bpf.o: $(ROOT)/kprobes/driver_kprobes.bpf.c $(ROOT)/common.h $(BUILD)/vmlinux.h
	clang $(BPF_CFLAGS) -c $< -o $@

$(BUILD)/proc_lifecycle.bpf.o: $(ROOT)/proc_lifecycle.bpf.c $(ROOT)/common.h $(BUILD)/vmlinux.h
	clang $(BPF_CFLAGS) -c $< -o $@

$(BUILD)/load_host_objects: load_host_objects.c | $(BUILD)
	$(CC) $(CFLAGS) -o $@ $< $(LIBS)

build: $(HOST_OBJECTS) $(BUILD)/load_host_objects

verify: build
	$(BUILD)/load_host_objects $(HOST_OBJECTS)

clean:
	$(RM) -r $(BUILD)
