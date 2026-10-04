SHELL := /bin/bash

ROOT          := ../../..
KERNELS_DIR   := $(ROOT)/kernels_to_trace
KERNELS_BUILD := $(KERNELS_DIR)/build
NVML_SRC_DIR  := ..

CC     := gcc
CFLAGS := -Wall -Wextra -O1 -pthread

# Auto-detect CUDA Toolkit — prefer versioned path if /usr/local/cuda symlink is missing.
CUDA_HOME ?= $(firstword $(wildcard /usr/local/cuda /usr/local/cuda-*))
ifeq ($(CUDA_HOME),)
    $(error CUDA Toolkit not found. Set CUDA_HOME=/path/to/cuda)
endif
NVML_INCLUDE := $(CUDA_HOME)/targets/$(shell uname -m)-linux/include
ifeq ($(wildcard $(NVML_INCLUDE)/nvml.h),)
    NVML_INCLUDE := $(CUDA_HOME)/include
endif
NVML_CFLAGS := -I$(NVML_INCLUDE) -Wno-deprecated-declarations

NVML_LIB_DIR ?=
NVML_LIBS    := $(if $(NVML_LIB_DIR),-L$(NVML_LIB_DIR)) -lnvidia-ml -ldl
NVCC         ?= $(if $(wildcard $(CUDA_HOME)/bin/nvcc),$(CUDA_HOME)/bin/nvcc,$(shell command -v nvcc 2>/dev/null))

BUILD := build

PROGRAM ?= k1

.PHONY: all build build-inputs test run-poller-test run-xid-test \
        run-xid-test-manual clean

all: build build-inputs

$(BUILD)/.d:
	@mkdir -p $(BUILD)
	@touch $@

$(BUILD)/poller_test: poller_test.c $(NVML_SRC_DIR)/poller.c $(NVML_SRC_DIR)/poller.h | $(BUILD)/.d
	$(CC) $(CFLAGS) -DWEDJAT_NVML_TEST $(NVML_CFLAGS) -o $@ poller_test.c $(NVML_SRC_DIR)/poller.c $(NVML_LIBS)

$(BUILD)/xid_test: xid_test.c $(NVML_SRC_DIR)/xid.c $(NVML_SRC_DIR)/xid.h $(NVML_SRC_DIR)/poller.c $(NVML_SRC_DIR)/poller.h | $(BUILD)/.d
	$(CC) $(CFLAGS) $(NVML_CFLAGS) -o $@ xid_test.c $(NVML_SRC_DIR)/xid.c $(NVML_SRC_DIR)/poller.c $(NVML_LIBS)

build: $(BUILD)/poller_test $(BUILD)/xid_test

build-inputs:
	@if [ -n "$(NVCC)" ]; then \
	  $(MAKE) -C $(KERNELS_DIR) build NVCC="$(NVCC)"; \
	else \
	  echo "SKIP CUDA fixture build (nvcc not found; GPU process polling test will be skipped)"; \
	fi

run-poller-test: build build-inputs
	@if [ ! -x "$(KERNELS_BUILD)/$(PROGRAM)" ]; then \
	  echo "SKIP poller_test (fixture $(KERNELS_BUILD)/$(PROGRAM) unavailable; install CUDA compiler nvcc to build it)"; \
	else \
	  echo "RUN  poller_test PROGRAM=$(PROGRAM)"; \
	  $(BUILD)/poller_test $(PROGRAM); rc=$$?; \
	  if   [ $$rc -eq 0  ]; then echo "PASS poller_test"; \
	  elif [ $$rc -eq 77 ]; then echo "SKIP poller_test (NVML/device capability unavailable)"; \
	  else echo "FAIL poller_test (exit $$rc)"; exit 1; fi; \
	fi

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
	@echo "NVML automated test targets completed"
	@echo "(xid coverage is plumbing-only — see README for manual fault test)"

clean:
	$(RM) -r $(BUILD)
