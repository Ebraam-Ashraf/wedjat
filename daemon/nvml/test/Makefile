SHELL := /bin/bash

ROOT          := ../../..
KERNELS_DIR   := $(ROOT)/kernels_to_trace
KERNELS_BUILD := $(KERNELS_DIR)/build
NVML_SRC_DIR  := ..

CC     := gcc
CFLAGS := -Wall -Wextra -O1

# Auto-detect CUDA Toolkit — prefer versioned path if /usr/local/cuda symlink is missing.
CUDA_HOME ?= $(firstword $(wildcard /usr/local/cuda /usr/local/cuda-*))
ifeq ($(CUDA_HOME),)
    $(error CUDA Toolkit not found. Set CUDA_HOME=/path/to/cuda)
endif
NVML_INCLUDE := $(CUDA_HOME)/targets/x86_64-linux/include
ifeq ($(wildcard $(NVML_INCLUDE)/nvml.h),)
    NVML_INCLUDE := $(CUDA_HOME)/include
endif
NVML_CFLAGS := -I$(NVML_INCLUDE) -Wno-deprecated-declarations

NVML_LIB_DIR ?= /usr/lib/x86_64-linux-gnu
NVML_LIBS    := -L$(NVML_LIB_DIR) -lnvidia-ml

BUILD := build

PROGRAM ?= k1

.PHONY: all build build-inputs test run-poller-test run-xid-test \
        run-xid-test-manual clean

all: build build-inputs

$(BUILD)/.d:
	@mkdir -p $(BUILD)
	@touch $@

$(BUILD)/poller_test: poller_test.c $(NVML_SRC_DIR)/poller.c $(NVML_SRC_DIR)/poller.h | $(BUILD)/.d
	$(CC) $(CFLAGS) $(NVML_CFLAGS) -Wl,--wrap=nvmlDeviceGetPowerUsage -o $@ poller_test.c $(NVML_SRC_DIR)/poller.c $(NVML_LIBS)

$(BUILD)/xid_test: xid_test.c $(NVML_SRC_DIR)/xid.c $(NVML_SRC_DIR)/xid.h | $(BUILD)/.d
	$(CC) $(CFLAGS) $(NVML_CFLAGS) -o $@ xid_test.c $(NVML_SRC_DIR)/xid.c $(NVML_LIBS)

build: $(BUILD)/poller_test $(BUILD)/xid_test

build-inputs:
	$(MAKE) -C $(KERNELS_DIR) build

run-poller-test: build build-inputs
	@test -x "$(KERNELS_BUILD)/$(PROGRAM)" || { \
		echo "missing fixture: $(KERNELS_BUILD)/$(PROGRAM)" >&2; \
		echo "  run: make -C kernels_to_trace build" >&2; \
		exit 2; \
	}
	@echo "RUN  poller_test PROGRAM=$(PROGRAM)"
	@$(BUILD)/poller_test $(PROGRAM); rc=$$?; \
	  if   [ $$rc -eq 0  ]; then echo "PASS poller_test"; \
	  elif [ $$rc -eq 77 ]; then echo "SKIP poller_test (NVML/device capability unavailable)"; \
	  else echo "FAIL poller_test (exit $$rc)"; exit 1; fi

run-xid-test: build
	@echo "RUN  xid_test"
	@$(BUILD)/xid_test; rc=$$?; \
	  if   [ $$rc -eq 0  ]; then echo "PASS xid_test"; \
	  elif [ $$rc -eq 77 ]; then echo "SKIP xid_test (not supported on this GPU/driver)"; \
	  else echo "FAIL xid_test (exit $$rc)"; exit 1; fi

run-xid-test-manual: build
	@echo ""
	@echo "MANUAL TEST — this blocks until a real Xid fault fires."
	@echo "  Run a GPU stress test in another terminal, or wait."
	@echo "  Watch:  dmesg -w | grep -i xid"
	@echo ""
	$(BUILD)/xid_test --manual

test: run-poller-test run-xid-test
	@echo "all NVML automated tests passed"
	@echo "(xid coverage is plumbing-only — see README for manual fault test)"

clean:
	$(RM) -r $(BUILD)
