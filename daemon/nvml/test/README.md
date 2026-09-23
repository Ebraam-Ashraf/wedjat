# NVML Tests

This directory tests the user-space NVML polling code in `daemon/nvml/`.

---

## This Is Not A BPF Test Directory

`uprobes/test` and `kprobes/test` load BPF objects, attach probes, and
assert on ring buffer events.

This directory does none of that.  `poller.c` and `xid.c` are plain C
files that call `libnvidia-ml.so` directly.  There is no kernel program,
no skeleton, no verifier.  These are integration tests against the real
NVML API running against a real GPU.

---

## Two Test Files

### poller_test — Layer 4 device and process stats

Tests `daemon/nvml/poller.c`.

Two sub-tests:

**Global sanity (no fixture needed):**
Calls `poller_snapshot_device()` and checks the returned values are in
plausible hardware ranges:
- `gpu_util` and `mem_util` are 0–100
- `temp_c` is 0–150°C
- `mem_total > 0`
- `mem_used <= mem_total`
- `uuid` is non-empty

These cannot be asserted against exact values — GPU state is not under
the test's control.

**Per-process fixture check:**
Spawns `k1` (CUDA Runtime API) with a known allocation size (256 MiB of
floats, controlled via `WEDJAT_N`).  Polls
`poller_snapshot_processes()` until the child's PID appears in
`nvmlDeviceGetComputeRunningProcesses_v3` output, or times out.

Asserts:
- Child PID appears within 20 × 150 ms = 3 seconds
- Reported `usedGpuMemory` is in `[alloc/2, alloc*4]` range

The range is intentionally loose — NVML includes driver and context
overhead that varies across driver versions.  The goal is "NVML is
reporting this process with a plausible value," not a byte-exact match.

### xid_test — Layer 4 hardware fault event path

Tests `daemon/nvml/xid.c`.

**Automated mode (default, included in `make test`):**
Creates an event set, registers for `nvmlEventTypeXidCriticalError`,
waits 500 ms.  No fault is happening.  Pass condition:
- `XID_WAIT_TIMEOUT` — wiring works, no event = correct
- `XID_WAIT_NOT_SUPPORTED` — GPU/driver does not support event
  registration, acceptable, not a failure

Any other result is a FAIL: the plumbing is broken.

**Manual mode (`--manual`, NOT in `make test`):**
Blocks indefinitely waiting for a real hardware fault.  A human runs
this while separately stressing the GPU or watching `dmesg` for NVRM
Xid messages.  If a fault fires, logs the full event and exits 0.
Never run in CI — depends on hardware behaving badly, which cannot be
scheduled.

---

## Requirements

- Real NVIDIA GPU + driver (`nvidia-smi` must work)
- `libnvidia-ml.so` on the linker path (from the NVIDIA driver package)
- `nvml.h` (from the CUDA Toolkit, usually at `/usr/local/cuda/include`)
- Root is **not** required for `poller_test` — NVML device stats are
  user-accessible.  `xid_test` may need root for
  `nvmlDeviceRegisterEvents` depending on driver config.

---

## Commands

```sh
# Build stub binaries (no NVML required — for CI or no-GPU machines)
make -C daemon/nvml/test build

# Build with real libnvidia-ml
make -C daemon/nvml/test build-nvml

# Build CUDA fixture binaries
make -C daemon/nvml/test build-inputs

# Run automated tests (stub mode by default)
make -C daemon/nvml/test test

# Run with real NVML (requires GPU + libnvidia-ml)
make -C daemon/nvml/test test USE_NVML=1

# Run just poller test
make -C daemon/nvml/test run-poller-test USE_NVML=1

# Run just xid plumbing test
make -C daemon/nvml/test run-xid-test USE_NVML=1

# Manual xid fault test — blocks until fault fires, NEVER in CI
make -C daemon/nvml/test run-xid-test-manual USE_NVML=1

# Override paths if CUDA / libnvidia-ml are in non-default locations
make -C daemon/nvml/test build-nvml CUDA_HOME=/usr/cuda NVML_LIB_DIR=/usr/lib64
```

---

## Current State

`poller.c` and `xid.c` have real function signatures but all NVML calls
are in TODO comments — not implemented yet.  Both test files compile and
run today in stub mode, printing SKIP for the NVML assertions.

When the implementation is ready:
```text
1. Uncomment the NVML TODO blocks in poller.c and xid.c.
2. make -C daemon/nvml/test build-nvml
3. make -C daemon/nvml/test test USE_NVML=1
```

---

## Adding A New Test

```text
1. Decide: is this a global stat (no fixture) or per-process (needs fixture)?
2. Write <name>_test.c using NVML_CHECK / ASSERT_TRUE / ASSERT_IN_RANGE.
3. Add build rules to Makefile: stub + nvml variants.
4. Add a run-<name>-test target.
5. If safe for automated runs, add it to the test: target.
   If it depends on unpredictable hardware behavior, keep it as a separate
   manual target like run-xid-test-manual.
6. Add a section to this README.
```

## Modifying A Test

If you change the expected allocation size in `poller_test.c`, the range
check `[alloc/2, alloc*4]` should stay as-is.  Do not tighten it to an
exact byte match — NVML's reported memory includes driver overhead that
is not under the test's control.

---

## Relationship to Other Test Directories

```text
uprobes/test/    BPF tests — host CPU: who called CUDA, what parameters
kprobes/test/    BPF tests — kernel driver: ioctl latency, UVM faults
device/test/     CUPTI tests — GPU SMs: utilization, warp stalls
nvml/test/       NVML tests — device totals: temp, power, VRAM, active PIDs
```

All four share the same fixture programs (`k1`, `k2`, `k8`) and the same
fork/exec/assert harness shape.
