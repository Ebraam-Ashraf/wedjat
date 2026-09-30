SHELL := /bin/bash

# `all` is the default goal. It has to be set explicitly: FORCE is declared
# early (the BPF flags stamp needs it) and a phony target with no recipe
# becomes the default goal otherwise, which makes a bare `make` do nothing.
.DEFAULT_GOAL := all
.PHONY: FORCE
FORCE:

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

# The device object is compiled exactly like the host probes, it just is never
# loaded into the kernel (bpftime loads it, see gpu/gpu_sm.bpf.c).
BPF_CFLAGS := -g -O2 -target bpf -D__TARGET_ARCH_$(ARCH) -D__bpf__ \
              -I$(BUILD) -I$(BPF_SRC_DIR)/..

HAVE_PKG_CONFIG := $(shell command -v pkg-config 2>/dev/null)
ifdef HAVE_PKG_CONFIG
    CFLAGS := -Wall -Wextra -O1 $(shell pkg-config --cflags libbpf 2>/dev/null)
    LIBS   := $(shell pkg-config --libs libbpf 2>/dev/null) -lelf -lz
else
    CFLAGS := -Wall -Wextra -O1
    LIBS   := -lbpf -lelf -lz
endif

.PHONY: all test repeat clean build-bpf build-tests build-inputs

all: build-bpf build-tests build-inputs

# ---------------------------------------------------------------------------
# BPF compilation + skeleton generation
# ---------------------------------------------------------------------------

$(BUILD)/.d:
	@mkdir -p $(BUILD)
	@touch $@

$(BUILD)/vmlinux.h: | $(BUILD)/.d
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@

$(BUILD)/.bpf-flags.stamp: FORCE | $(BUILD)/.d
	@printf '%s\n' '$(BPF_CFLAGS)' > $@.tmp
	@if cmp -s $@.tmp $@; then rm -f $@.tmp; else mv $@.tmp $@; fi

$(BUILD)/%.bpf.o: $(BPF_SRC_DIR)/%.bpf.c \
    $(BPF_SRC_DIR)/../common.h $(BUILD)/vmlinux.h $(BUILD)/.bpf-flags.stamp | $(BUILD)/.d
	$(BPF_CC) $(BPF_CFLAGS) -c $< -o $@

$(BUILD)/%.skel.h: $(BUILD)/%.bpf.o
	$(BPFTOOL) gen skeleton $< > $@

# Compiling is all we can do with the device object on this machine: attaching
# it needs a bpftime build with CUDA attach support. The checks
# below are what build-bpf gets out of the object — both probe sections
# present, and no host map carried into the GPU program.
build-bpf: $(BUILD)/gpu_sm.bpf.o $(BUILD)/gpu_sm.skel.h
	@llvm-objdump -h $(BUILD)/gpu_sm.bpf.o | grep -q ' kprobe/' \
		|| { echo "FAIL: no kprobe section"; exit 1; }
	@llvm-objdump -h $(BUILD)/gpu_sm.bpf.o | grep -q ' kretprobe/' \
		|| { echo "FAIL: no kretprobe section"; exit 1; }
	@llvm-objdump -t $(BUILD)/gpu_sm.bpf.o | grep -qw dev_events_pipe \
		|| { echo "FAIL: dev_events_pipe missing"; exit 1; }
	@! llvm-objdump -t $(BUILD)/gpu_sm.bpf.o \
		| grep -qwE 'events_pipe|inflight_map|tid_to_device|ctx_to_device' \
		|| { echo "FAIL: host map leaked into the device object"; exit 1; }
	@echo "PASS build-bpf (both probes, host maps absent)"

# ---------------------------------------------------------------------------
# Test harness compilation
# ---------------------------------------------------------------------------

$(BUILD)/gpu_sm_test: gpu_sm_test.c \
    $(BPF_SRC_DIR)/../common.h \
    $(BUILD)/gpu_sm.skel.h | $(BUILD)/.d
	$(CC) $(CFLAGS) -I$(BUILD) -I$(BPF_SRC_DIR)/.. -o $@ $< $(LIBS)

build-tests: $(BUILD)/gpu_sm_test

# ---------------------------------------------------------------------------
# CUDA fixture inputs
# ---------------------------------------------------------------------------

build-inputs:
	$(MAKE) -C $(KERNELS_DIR) build

# ---------------------------------------------------------------------------
# Test execution
# ---------------------------------------------------------------------------
#
# The harness needs bpftime's syscall server preloaded, and it skips (exit 77)
# on any machine that doesn't have it. Override the two paths if yours are
# somewhere else:
#   make test BPFTIME_SERVER=/path/libbpftime-syscall-server.so
#   make test BPFTIME_AGENT=/path/libbpftime-agent.so

BPFTIME_SERVER ?= $(HOME)/.bpftime/libbpftime-syscall-server.so
BPFTIME_AGENT  ?= $(HOME)/.bpftime/libbpftime-agent.so

# bpftime narrates its own bootstrap at [info] — cuobjdump, ptxas, module
# loads, fatbin cache hits — once per fixture, four times over.
#
# BPFTIME_LOG_OUTPUT moves most of it to $(BPFTIME_LOG), but not all of it:
#   - ptxas is a subprocess and prints "Extracting PTX file and ptxas options"
#     straight to our stdout
#   - the ptxpass plugin .so installs its own spdlog logger before
#     bpftime_set_logger runs, so "[ptxpass] ... matched=N" and the
#     bpftime_vm_compat warning land on stderr too
# cuda_actions_test and host_ctx_test print none of this because they hook host
# APIs and never patch PTX, so they have nothing to leak. Layer 5 has to.
#
# Both leftovers are spdlog/ptxas lines: they start with a timestamp or the word
# "Extracting". Filter them from the live terminal by default. tee keeps the
# complete process output in test.log for diagnosis.
#
#   make test                      # filtered live output; raw output in build/test.log
#   make test BPFTIME_LOG=console  # also stream bpftime's own narration
BPFTIME_LOG ?= $(abspath $(BUILD))/bpftime.log

BPFTIME_RUN = sudo env LD_PRELOAD=$(BPFTIME_SERVER) BPFTIME_AGENT=$(BPFTIME_AGENT) \
	BPFTIME_MAP_GPU_THREAD_COUNT=8192 DUMP="$(DUMP)" LIBBPF_DEBUG="$(LIBBPF_DEBUG)" \
	BPFTIME_LOG_OUTPUT=$(BPFTIME_LOG) SPDLOG_LEVEL=info \
	WEDJAT_N=8192 WEDJAT_ITERS=3 \
	$(BUILD)/gpu_sm_test $(KERNELS_BUILD)

# bpftime's own lines: spdlog timestamps, or ptxas's banner.
BPFTIME_NOISE = ^\[[0-9]|^Extracting

test: all
	@# bpftime keeps its handler and map tables in /dev/shm/bpftime_maps_shm and
	@# never unlinks it. A run that dies mid-load leaves that segment behind,
	@# and the next run reuses it via boost's open_or_create, which does NOT
	@# resize an existing segment -- so open_and_load() walks a stale
	@# handler_manager full of dangling map entries and segfaults. This is what
	@# makes the failure look nondeterministic: a fresh boot works once, then
	@# every later run inherits the corpse. Clear it first, every time.
	sudo rm -f /dev/shm/bpftime_maps_shm
	@mkdir -p $(BUILD)
	@set -o pipefail; \
	$(BPFTIME_RUN) 2>&1 | tee "$(BUILD)/test.log" | \
	if [[ "$(BPFTIME_LOG)" == "console" ]]; then cat; \
	else grep --line-buffered -vE '$(BPFTIME_NOISE)'; fi; \
	rc=$$?; \
	echo; echo "test log: $(abspath $(BUILD))/test.log"; \
	if [[ "$(BPFTIME_LOG)" != "console" ]]; then echo "bpftime log: $(BPFTIME_LOG)"; fi; \
	exit $$rc

# Repeat the whole run and print the tally. A single PASS proves one run
# worked; this is what shows the telemetry is not a coin flip, which is the
# question layer 5 actually has to answer.
#
#   make repeat              # 20 runs, the default
#   make repeat REPEAT=50
#   make repeat DUMP=1       # keep the per-event dump
#
# Each run's own output is kept in run-N.log. bpftime's log is a rotating file
# under $(BUILD), so it accumulates across runs rather than being overwritten --
# grep it for the evidence lines:
#   grep -E 'ptpass|Copying map fd|Attach successfully' $(BUILD)/bpftime.log
REPEAT ?= 20
repeat:
	@mkdir -p $(BUILD)
	@pass=0; fail=0; \
	for i in $$(seq 1 $(REPEAT)); do \
	  if $(MAKE) --no-print-directory test >$(BUILD)/run-$$i.log 2>&1; then \
	    pass=$$((pass+1)); printf '.'; \
	  else \
	    fail=$$((fail+1)); printf 'F'; \
	  fi; \
	done; \
	echo; \
	echo "runs=$(REPEAT) pass=$$pass fail=$$fail  (logs: $(BUILD)/run-*.log)"; \
	test $$fail -eq 0

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean:
	$(RM) -r $(BUILD)
	$(MAKE) -C $(KERNELS_DIR) clean
