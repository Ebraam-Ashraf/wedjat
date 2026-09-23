# Kprobe Tests

This directory tests Wedjat's Layer 3 eBPF kprobe programs.

---

## How This Differs From uprobes/test

`uprobes/test` attaches to **userspace** symbols in `libcuda.so`.
This directory attaches to **kernel** symbols in `nvidia.ko` and `nvidia-uvm.ko`,
and to Linux kernel tracepoints.

The test harness shape is identical: fork a CUDA fixture, poll a ring buffer,
assert on what the probe captured.  The difference is:

- These probes fire system-wide — not just for the fixture process.  We must
  filter on `pid` in the ring buffer callback.
- NVIDIA kernel symbols (`nvidia_unlocked_ioctl`, `uvm_*`) are only present
  when the NVIDIA kernel modules are loaded.  The test checks `/proc/kallsyms`
  first and prints `SKIP` if the symbol is absent, rather than crashing.
- Running requires root (`CAP_BPF` + `CAP_PERFMON`) and a loaded `nvidia.ko`.

---

## BPF Program Under Test

```text
driver_kprobes.bpf.c  (one directory up: daemon/ebpf/kprobes/)

Probes:
  kprobe  + kretprobe  nvidia_unlocked_ioctl   (all CUDA driver calls)
  kprobe               uvm_vm_fault_entry       (UVM page faults)
  kprobe               uvm_migrate              (managed memory migration)
  tracepoint           sched/sched_switch       (GPU thread preemption)
```

---

## CUDA Inputs

Same fixtures as uprobes/test — they live in `kernels_to_trace/build/`.

```text
k1    CUDA Runtime API: kernel launches
k2    CUDA Driver API:  cuMemAlloc / cuMemFree / cuStreamSynchronize
```

Both call into `nvidia_unlocked_ioctl` under the hood.  `k2` is the default
because its Driver API calls map more directly to observable driver ioctls.

---

## Commands

```sh
# Build everything (BPF object → skeleton → test binary → CUDA fixtures)
make -C daemon/ebpf/kprobes/test build
make -C daemon/ebpf/kprobes/test build-inputs

# Run all tests
sudo make -C daemon/ebpf/kprobes/test test

# Run against one specific fixture
sudo make -C daemon/ebpf/kprobes/test test-one PROGRAM=k2
sudo make -C daemon/ebpf/kprobes/test test-one PROGRAM=k1

# See available fixtures
make -C daemon/ebpf/kprobes/test list
```

---

## What `test-one` Does (once BPF code exists)

```text
Level 1+2  Build and load driver_kprobes.bpf.o.
           Does clang compile it? Does the kernel verifier accept it?

Level 3    Check /proc/kallsyms for nvidia_unlocked_ioctl.
           Attach kprobes (auto-attach from SEC() names in the skeleton).
           Run kernels_to_trace/build/k2.
           Does at least one ioctl event arrive in the ring buffer?

Level 4    Read the event.
           Assert pid matches k2's pid.
           Assert latency_ns > 0.
           Assert ioctl_counts map has a non-zero entry for k2's pid.
```

---

## Current State

`driver_kprobes.bpf.c` is written with real BPF code.  The skeleton
(`driver_kprobes.skel.h`) does not exist yet — it is generated from the
compiled `.bpf.o` file.

Test file in this directory:

```text
driver_kprobes_test.c   real harness shape, BPF loader/attach in TODO blocks,
                        kallsyms check is real and active today
```

When the BPF implementation is ready:

```text
1. make -C daemon/ebpf/kprobes/test build-bpf
     -> compiles driver_kprobes.bpf.o
     -> generates driver_kprobes.skel.h

2. Uncomment the skeleton loader/attach/ringbuf blocks in driver_kprobes_test.c

3. sudo make -C daemon/ebpf/kprobes/test test
```

---

## Adding A New Probe

```text
1. Add the SEC("kprobe/new_symbol") probe to driver_kprobes.bpf.c
2. Add a test function in driver_kprobes_test.c following the same shape
3. Call it from main()
4. Add new_symbol to the kallsyms check in the test
```
