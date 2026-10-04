# nvml/test

Tests for `daemon/nvml/poller.c` and `daemon/nvml/xid.c`.

Not a BPF test directory — no skeleton, no verifier.  These are plain C
integration tests that link against `libnvidia-ml.so` and run against a real GPU.

---

## Build & Run

```sh
cd daemon/nvml/test

make build             # build binaries → build/
make build-inputs      # CUDA fixture binaries (k1, k2, …; skipped if nvcc is unavailable)

make test              # run automated tests
make run-poller-test
make run-xid-test

# manual xid fault test — blocks until a real fault fires, NEVER in CI
make run-xid-test-manual

# override paths if CUDA / libnvidia-ml are not in default locations
make build CUDA_HOME=/usr/cuda NVML_LIB_DIR=/usr/lib64
```

---

## What The Tests Do

### poller_test — device and process stats

Tests `poller.c`.  Two sub-tests run in sequence:

**1. device snapshot** — calls `poller_snapshot_device(0, …)`, asserts plausible ranges:
- `gpu_util`, `mem_util` in 0–100
- `temp_c` in 0–150 °C
- `mem_total > 0`, `mem_used <= mem_total`
- `uuid` non-empty

**2. process snapshot** — spawns a CUDA fixture (`k1`) allocating 256 MiB,
polls `poller_snapshot_processes()` until the child pid appears, then asserts:
- pid found within 20 × 150 ms = 3 s
- `usedGpuMemory` in `[alloc/2, alloc*4]` — loose range to absorb driver overhead

```text
== poller_test ==
PASS device_snapshot (gpu=12% temp=48°C mem=2048/8192MiB uuid=GPU-a1b2c3d4...)
PASS process_snapshot (pid=12345 used=258MiB alloc=256MiB)

poller_test: PASS
```

### xid_test — hardware fault event plumbing

Tests `xid.c` event wiring, not a real fault.

**Automated mode (default, included in `make test`):**
Creates an event set for device 0, registers for `nvmlEventTypeXidCriticalError`,
waits 500 ms.  Pass conditions:
- `XID_WAIT_TIMEOUT` — wiring works, no fault = correct
- `XID_WAIT_NOT_SUPPORTED` → SKIP, not FAIL — some GPUs or driver configs
  do not support event registration

**Manual mode (`--manual`, NOT in `make test`):**
Blocks until a real hardware fault fires.  Run while stressing the GPU
or watching `dmesg -w | grep -i xid`.  Logs the captured event and exits 0.

```text
== xid_test ==
PASS xid_event_plumbing (automated — clean timeout)

xid_test: PASS
```

---

## Exit Codes

| Code | Meaning |
|------|---------|
| `0`  | PASS |
| `1`  | FAIL |
| `77` | SKIP (no GPU, NVML unavailable, or feature not supported) |

---

## Requirements

- Real NVIDIA GPU + driver (`nvidia-smi` must work)
- `libnvidia-ml.so` (from the NVIDIA driver package — usually `/usr/lib/x86_64-linux-gnu`)
- `nvml.h` (from the CUDA Toolkit — usually `/usr/local/cuda/include`)
- `nvcc` is needed for the process-polling fixture; without it, that test is
  reported as skipped while the other NVML tests continue.
- Root is **not** required for `poller_test`
- Root **may** be required for `xid_test` (`nvmlDeviceRegisterEvents`) depending on driver config

---

## Relationship to Other Test Directories

```text
uprobes/test/    BPF tests — host CPU: who called CUDA, what parameters
kprobes/test/    BPF tests — kernel driver: ioctl latency, UVM faults
nvml/test/       NVML tests — device totals: temp, power, VRAM, active PIDs
```

All share the same fixture programs (`k1`, `k2`, …) and the same fork/exec/assert shape.

---

## Adding a New Test

```text
1. Write <name>_test.c — use PASS/FAIL/SKIP macros, numbered main flow.
2. Add stub + nvml build rules to Makefile.
3. Add a run-<name>-test target.
4. Add it to test: if safe for automated runs; otherwise make it a manual target.
5. Add a section here.
```
