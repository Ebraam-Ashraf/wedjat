# Uprobe Tests

This directory tests Wedjat's eBPF uprobe programs.

Two developer roles, completely separate workflows:

---

## CUDA Developer

Lives in `kernels_to_trace/`. Writes normal CUDA programs, builds them, runs
them. Nothing here is relevant to them.

```sh
make -C kernels_to_trace build
make -C kernels_to_trace run-k1
```

---

## eBPF Developer

Lives here. Tests whether the eBPF probes in `daemon/ebpf/uprobes/` actually
capture the right events when a CUDA program runs.

The CUDA programs from `kernels_to_trace/build/` are trigger inputs — they are
not tests. They are what makes real CUDA API calls happen so the probes have
something to catch.

Tests are written in C using libbpf.

### Test Matrix

Tests are organized as: **one eBPF program × one CUDA input program.**

```text
BPF programs:
  host_ctx        (host_ctx.bpf.c — Layer 1 identity)
  cuda_actions    (cuda_actions.bpf.c — Layer 2 CUDA actions)

CUDA input programs:
  k1              (CUDA Runtime API: kernel launches)
  k2              (CUDA Driver API: memory alloc/free/sync)
```

### Commands

```sh
# Test all BPF programs against all CUDA inputs
make -C daemon/ebpf/uprobes/test test

# Test one BPF program against all CUDA inputs
make -C daemon/ebpf/uprobes/test test-host-ctx
make -C daemon/ebpf/uprobes/test test-cuda-actions

# Test one specific combination
make -C daemon/ebpf/uprobes/test test-one BPF=host_ctx PROGRAM=k1
make -C daemon/ebpf/uprobes/test test-one BPF=cuda_actions PROGRAM=k2

# See all available BPF programs and CUDA inputs
make -C daemon/ebpf/uprobes/test list
```

### What `test-one` Does (once BPF code exists)

Example: `make test-one BPF=host_ctx PROGRAM=k1`

```text
Level 1+2  Build and load host_ctx.bpf.o.
           Does clang compile it? Does the kernel verifier accept it?

Level 3    Attach host_ctx uprobes to libcuda.so:cuLaunchKernel.
           Run kernels_to_trace/build/k1 as a subprocess.
           Does at least one event arrive in the ring buffer?

Level 4    Read the event.
           Assert pid matches k1's pid.
           Assert comm is non-empty.
           Assert timestamp_ns is non-zero.
```

---

## Current State

The eBPF programs (`host_ctx.bpf.c`, `cuda_actions.bpf.c`) are not implemented
yet — they contain design comments only. Running `make test` will build the CUDA
inputs and then print a TODO stub for each BPF×CUDA combination.

Test files in this directory are stubs until the BPF loader exists:

```text
host_ctx_test.c    — stub, shows intended test shape (C/libbpf)
```

When the BPF implementation exists:
1. Generate the skeleton: `bpftool gen skeleton host_ctx.bpf.o > host_ctx.skel.h`
2. Uncomment the real loader/attach/ringbuf code in `host_ctx_test.c`.
3. Remove the SKIP placeholder.

---

## Adding A New CUDA Input

```text
1. Add kernels_to_trace/k3.cu
2. Add build/k3 to kernels_to_trace/Makefile
3. Add k3 to CUDA_PROGRAMS in this Makefile
4. Extend the relevant test .c file to also exec build/k3
```

## Adding A New BPF Program Under Test

```text
1. Add daemon/ebpf/uprobes/newprobe.bpf.c
2. Add newprobe to BPF_PROGRAMS in this Makefile
3. Add a newprobe_test.c here following the same shape as host_ctx_test.c
```
