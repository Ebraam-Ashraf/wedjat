# Layer 5 Tests

This directory is the test surface for `daemon/ebpf/device/`.

---

## This Is Not A BPF Test Directory

`uprobes/test` and `kprobes/test` contain BPF test harnesses that load
`.bpf.o` objects, attach probes, and assert on ring buffer events.

This directory does **not** do that.

`device_sm.bpf.c` has no BPF code — Linux eBPF cannot run inside a GPU
kernel. See `device/README.md` for the full explanation and the four
implementation options.

The test surface here is **CUPTI** — NVIDIA's Compute Unified Device
Infrastructure profiling API, which is the realistic first path to
GPU-side telemetry (SM utilization, warp stall reasons, kernel activity).

---

## What Is Tested

```text
cupti_sm_test.c

  Level 3    CUPTI subscribes to cuLaunchKernel callbacks, runs a CUDA fixture.
  Level 4a   At least one kernel launch was observed via the callback.
  Level 4b   cuDeviceGetAttribute reports SM count > 0.
```

The fixture is the same programs as all other Wedjat tests: `k1`, `k2`,
`k8` from `kernels_to_trace/build/`.

---

## Two Build Modes

The Makefile supports two builds:

```text
stub (default)     No CUDA/CUPTI dependency.
                   fork/exec/waitpid skeleton is real.
                   CUPTI blocks are in TODO comments.
                   Use this in CI or on a machine without a GPU.

cupti              Links against libcupti and libcuda.
                   Real CUPTI integration.
                   Requires CUDA Toolkit at CUDA_HOME (default /usr/local/cuda).
                   Use this on a machine with a GPU to run actual Level 4 checks.
```

---

## Commands

```sh
# Build stub (no GPU required)
make -C daemon/ebpf/device/test build

# Build with real CUPTI
make -C daemon/ebpf/device/test build-cupti

# Build CUDA fixtures
make -C daemon/ebpf/device/test build-inputs

# Run stub tests against all CUDA inputs (no GPU)
make -C daemon/ebpf/device/test test

# Run one stub test
make -C daemon/ebpf/device/test test-one PROGRAM=k1

# Run real CUPTI test (requires GPU + root)
sudo make -C daemon/ebpf/device/test test-one PROGRAM=k1 USE_CUPTI=1

# Override CUDA Toolkit path
make -C daemon/ebpf/device/test build-cupti CUDA_HOME=/usr/cuda
```

---

## Current State

CUPTI blocks in `cupti_sm_test.c` are in TODO comments.
The stub compiles and runs the fixture today — it just prints SKIP for
the CUPTI assertions.

When CUPTI is integrated:
```text
1. Uncomment the cuptiSubscribe / cuptiEnableCallback blocks.
2. Uncomment the cuDeviceGetAttribute block.
3. make -C daemon/ebpf/device/test build-cupti
4. sudo make -C daemon/ebpf/device/test test-one PROGRAM=k1 USE_CUPTI=1
```

---

## Relationship to the Other Test Directories

```text
uprobes/test/    BPF tests — what did the CUDA host thread do?
kprobes/test/    BPF tests — what did the kernel driver do?
device/test/     CUPTI tests — what did the GPU SMs do?
```

All three share the same fixture programs (`k1`, `k2`, `k8`) and the
same fork/exec/assert harness shape. Only the observation mechanism
differs: BPF ring buffer vs CUPTI callback.
