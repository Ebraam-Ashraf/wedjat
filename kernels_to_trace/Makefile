SHELL := /bin/bash

NVCC ?= nvcc
BUILD_DIR ?= build
CUDA_STD ?= c++17
DETECTED_CC := $(shell nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | head -n1 | tr -d '.')
CUDA_ARCH ?= $(if $(DETECTED_CC),sm_$(DETECTED_CC),)
ARCH_FLAGS := $(if $(CUDA_ARCH),-arch=$(CUDA_ARCH),)
NVCCFLAGS ?= -O2 -lineinfo -std=$(CUDA_STD) $(ARCH_FLAGS)
LDFLAGS ?=
LDLIBS ?= -lcuda

PROGRAMS := $(BUILD_DIR)/k1 $(BUILD_DIR)/k2 $(BUILD_DIR)/k8
STAMP := $(BUILD_DIR)/.dir

.PHONY: help all build check-tools run-k1 run-k2 run-k8 smoke clean

help:
	@echo "CUDA trace fixtures"
	@echo "  make build       Build all CUDA fixture binaries into $(BUILD_DIR)/"
	@echo "  make run-k1      Run basic kernel-launch fixture"
	@echo "  make run-k2      Run driver-API memory allocation fixture"
	@echo "  make run-k8      Run multi-stream concurrent launch fixture"
	@echo "  make smoke       Build and run k1/k2/k8 with small iteration counts"
	@echo "  make clean       Remove generated fixture binaries"
	@echo ""
	@echo "Build knobs:"
	@echo "  CUDA_ARCH=$(CUDA_ARCH)      GPU arch passed to nvcc, override if needed"
	@echo "  NVCC=$(NVCC)                CUDA compiler"
	@echo ""
	@echo "Runtime knobs:"
	@echo "  WEDJAT_ITERS=10       Number of fixture loop iterations"
	@echo "  WEDJAT_SLEEP_MS=100   Sleep between iterations"
	@echo "  WEDJAT_N=1048576      Element count / allocation size basis"

all: build

build: check-tools $(PROGRAMS)

check-tools:
	@command -v $(NVCC) >/dev/null 2>&1 || { \
		echo "nvcc not found. Install CUDA Toolkit or set NVCC=/path/to/nvcc." >&2; \
		exit 127; \
	}

$(STAMP):
	@mkdir -p $(BUILD_DIR)
	@touch $@

$(BUILD_DIR)/%: %.cu $(STAMP)
	$(NVCC) $(NVCCFLAGS) $(LDFLAGS) -o $@ $< $(LDLIBS)

run-k1: $(BUILD_DIR)/k1
	$(BUILD_DIR)/k1

run-k2: $(BUILD_DIR)/k2
	$(BUILD_DIR)/k2

run-k8: $(BUILD_DIR)/k8
	$(BUILD_DIR)/k8

smoke: build
	WEDJAT_ITERS=2 WEDJAT_SLEEP_MS=10 $(BUILD_DIR)/k1
	WEDJAT_ITERS=2 WEDJAT_SLEEP_MS=10 $(BUILD_DIR)/k2
	WEDJAT_ITERS=2 WEDJAT_SLEEP_MS=10 $(BUILD_DIR)/k8

clean:
	$(RM) -r $(BUILD_DIR)
