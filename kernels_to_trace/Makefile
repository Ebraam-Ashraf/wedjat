# Auto-detect nvcc: prefer whatever is in PATH, then fall back to common
# CUDA install locations. This handles the common case where nvcc exists but
# is not in sudo's stripped PATH.
ifeq ($(shell command -v nvcc 2>/dev/null),)
    NVCC := $(firstword $(wildcard \
        /usr/local/cuda/bin/nvcc \
        /usr/local/cuda-*/bin/nvcc \
        /usr/cuda/bin/nvcc \
        /opt/cuda/bin/nvcc))
    ifeq ($(NVCC),)
        $(error nvcc not found. Install the CUDA toolkit or set NVCC=/path/to/nvcc)
    endif
else
    NVCC ?= nvcc
endif
BUILD := build

# bpftime's CUDA attach works by extracting the PTX out of a fatbin, patching
# it, and recompiling. "-arch=native" embeds SASS only, so cuobjdump reports
# "No PTX file found" and there is nothing for bpftime to patch — the probes
# attach but never execute. Embed PTX for the local arch as well.
#
# Derive the virtual arch from -arch=native so this is not pinned to one GPU.
# Falls back to 86 (the arch bpftime's PTX pass compiles for, see
# ptxpass::compile_ebpf_to_ptx_from_words(..., "sm_61", ...)) if the probe
# below does not work.
NATIVE_ARCH := $(shell $(NVCC) -arch=native --dryrun -c /dev/null -o /dev/null \
                  2>&1 | grep -o 'sm_[0-9]*' | head -1)
VIRTUAL_ARCH := compute_$(patsubst sm_%,%,$(if $(NATIVE_ARCH),$(NATIVE_ARCH),sm_86))

NVCCFLAGS := -arch=native --cudart shared -g \
            -gencode arch=$(VIRTUAL_ARCH),code=$(VIRTUAL_ARCH)

.PHONY: all build run clean

# 'build' is a phony alias for 'all'.
# Do NOT list $(BUILD) as a prerequisite here — that would clash with .PHONY: build
# since $(BUILD) expands to 'build', causing a circular dependency.
# The mkdir is handled inline in the pattern rule below instead.
all: $(patsubst %.cu,$(BUILD)/%,$(wildcard k*.cu))
build: all

$(BUILD)/%: %.cu
	@mkdir -p $(BUILD)
	$(NVCC) $(NVCCFLAGS) -o $@ $<

run: all
	@for prog in $(BUILD)/*; do \
		echo "--- Running $$prog ---"; \
		$$prog; \
	done

clean:
	rm -rf $(BUILD)
