KDIR  := ../../../../kernels_to_trace
BUILD := build
ARCH  := $(shell uname -m | sed 's/x86_64/x86/;s/aarch64/arm64/')

.PHONY: all run clean
.SECONDARY:

all: $(BUILD)/cuda_actions_test $(BUILD)/host_ctx_test \
     $(patsubst $(KDIR)/%.cu,$(BUILD)/%,$(wildcard $(KDIR)/k*.cu))

$(BUILD):
	mkdir -p $@

# kernel binaries
$(BUILD)/%: $(KDIR)/%.cu | $(BUILD)
	nvcc -o $@ $<

# vmlinux.h
$(BUILD)/vmlinux.h: | $(BUILD)
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@

# BPF objects
$(BUILD)/%.bpf.o: ../%.bpf.c ../../common.h $(BUILD)/vmlinux.h | $(BUILD)
	clang -g -O2 -target bpf -D__TARGET_ARCH_$(ARCH) -I$(BUILD) -I../.. -c $< -o $@

# skeletons
$(BUILD)/%.skel.h: $(BUILD)/%.bpf.o
	bpftool gen skeleton $< > $@

# test binaries
$(BUILD)/cuda_actions_test: cuda_actions_test.c ../../common.h $(BUILD)/cuda_actions.skel.h
	$(CC) -g -Wall -I$(BUILD) -I../.. $< -lbpf -lelf -lz -o $@

$(BUILD)/host_ctx_test: host_ctx_test.c ../../common.h $(BUILD)/host_ctx.skel.h
	$(CC) -g -Wall -I$(BUILD) -I../.. $< -lbpf -lelf -lz -o $@

# run both tests
run: all
	sudo env LIBCUDA="$(LIBCUDA)" DUMP="$(DUMP)" $(BUILD)/cuda_actions_test $(BUILD)
	sudo env LIBCUDA="$(LIBCUDA)" DUMP="$(DUMP)" $(BUILD)/host_ctx_test     $(BUILD)

clean:
	rm -rf $(BUILD)
