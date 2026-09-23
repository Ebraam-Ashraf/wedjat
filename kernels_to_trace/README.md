# CUDA Programs To Trace

This directory contains plain CUDA programs that Wedjat observes.

These are not tests. They are normal CUDA programs. A CUDA developer adds a
new `kN.cu`, builds it, runs it, and watches it do GPU work. No pass/fail.
No relationship to probes on their own.

The eBPF tests that use these programs as trigger inputs live in:

```text
daemon/ebpf/uprobes/test/
```

---

## Quick Start

```sh
# Build all programs into build/
make -C kernels_to_trace build

# Run one directly
kernels_to_trace/build/k1
kernels_to_trace/build/k2

# Or via make
make -C kernels_to_trace run-k1
make -C kernels_to_trace run-k2

# Quick smoke run (2 iterations, fast)
make -C kernels_to_trace smoke
```

All binaries land in `kernels_to_trace/build/`.

---

## Runtime Knobs

Every program reads these environment variables:

```text
WEDJAT_ITERS       loop count           default 20
WEDJAT_SLEEP_MS    ms between iters     default 100
WEDJAT_N           element count        default 1048576
```

Example:

```sh
WEDJAT_ITERS=5 WEDJAT_SLEEP_MS=250 kernels_to_trace/build/k1
```

---

## Adding A New Program

### 1. Create the .cu file

Name it `kN.cu` where N is the next number. Copy the structure from an existing
program. The rules every program must follow:

**Use `env_int()` for all runtime knobs.**

```cpp
static int env_int(const char *name, int fallback) {
    const char *value = std::getenv(name);
    if (value == nullptr || value[0] == '\0') return fallback;
    char *end = nullptr;
    long parsed = std::strtol(value, &end, 10);
    if (end == value || parsed <= 0) return fallback;
    return static_cast<int>(parsed);
}

// then at the top of main():
const int iters    = env_int("WEDJAT_ITERS",    20);
const int sleep_ms = env_int("WEDJAT_SLEEP_MS", 100);
const int n        = env_int("WEDJAT_N",        1 << 20);
```

**Print the three required lines to stdout.**

```cpp
// On startup — one line, fixed format:
std::printf("WEDJAT_FIXTURE name=k3_your_name n=%d bytes=%zu iters=%d\n",
            n, bytes, iters);

// Once per loop iteration:
std::printf("WEDJAT_FIXTURE_EVENT name=k3_your_name iter=%d\n", i);
std::fflush(stdout);

// On clean exit:
std::printf("WEDJAT_FIXTURE_DONE name=k3_your_name\n");
```

These lines are how eBPF tests know what the program triggered. Keep the name
consistent across all three: `k3_your_name` in all of them.

**Use the right error-check macro.**

Runtime API (`#include <cuda_runtime.h>`):
```cpp
#define CUDA_CHECK(call) \
    do { \
        cudaError_t err__ = (call); \
        if (err__ != cudaSuccess) { \
            std::fprintf(stderr, "CUDA_CHECK failed %s:%d: %s\n", \
                         __FILE__, __LINE__, cudaGetErrorString(err__)); \
            return 1; \
        } \
    } while (0)
```

Driver API (`#include <cuda.h>`):
```cpp
#define CU_CHECK(call) \
    do { \
        CUresult res__ = (call); \
        if (res__ != CUDA_SUCCESS) { \
            const char *name__ = "unknown", *desc__ = "unknown"; \
            cuGetErrorName(res__, &name__); \
            cuGetErrorString(res__, &desc__); \
            std::fprintf(stderr, "CU_CHECK failed %s:%d: %s (%s)\n", \
                         __FILE__, __LINE__, name__, desc__); \
            return 1; \
        } \
    } while (0)
```

Use `CUDA_CHECK` for Runtime API calls and `CU_CHECK` for Driver API calls.
Do not mix them in the same program.

**Sleep between iterations** so probes have time to drain:

```cpp
if (sleep_ms > 0)
    std::this_thread::sleep_for(std::chrono::milliseconds(sleep_ms));
```

### 2. Add it to the Makefile

Open `kernels_to_trace/Makefile`. Two changes:

**Add the binary to the PROGRAMS list:**

```makefile
# before
PROGRAMS := $(BUILD_DIR)/k1 $(BUILD_DIR)/k2

# after
PROGRAMS := $(BUILD_DIR)/k1 $(BUILD_DIR)/k2 $(BUILD_DIR)/k3
```

**Add run and smoke targets:**

```makefile
run-k3: $(BUILD_DIR)/k3
	$(BUILD_DIR)/k3
```

Also add `run-k3` to the `.PHONY` line and mention it in the `help` target.

For smoke, add the new binary to the existing `smoke` target:

```makefile
smoke: build
	WEDJAT_ITERS=2 WEDJAT_SLEEP_MS=10 $(BUILD_DIR)/k1
	WEDJAT_ITERS=2 WEDJAT_SLEEP_MS=10 $(BUILD_DIR)/k2
	WEDJAT_ITERS=2 WEDJAT_SLEEP_MS=10 $(BUILD_DIR)/k3
```

### 3. Build and run it

```sh
make -C kernels_to_trace build
make -C kernels_to_trace run-k3
```

You should see your `WEDJAT_FIXTURE`, `WEDJAT_FIXTURE_EVENT`, and
`WEDJAT_FIXTURE_DONE` lines on stdout.

### 4. Update this README

Add a section under "Current Programs" describing what your program triggers,
following the same format as k1 and k2 below.

---

## Modifying An Existing Program

You can change anything about the GPU work a program does — kernel logic,
allocation sizes, loop structure, added streams, etc. The only things to keep
stable are:

- The `WEDJAT_FIXTURE` / `WEDJAT_FIXTURE_EVENT` / `WEDJAT_FIXTURE_DONE` print
  lines and their `name=` value. eBPF tests match on this name.
- Support for `WEDJAT_ITERS`, `WEDJAT_SLEEP_MS`, `WEDJAT_N` via `env_int()`.
- Returning `0` on success and `1` on error.

If you change which CUDA API calls the program makes (e.g. adding
`cuStreamSynchronize` calls to k1), update the "Useful for triggering" list in
this README so eBPF developers know which fixture to use for which probe.

After modifying, rebuild and verify:

```sh
make -C kernels_to_trace build
make -C kernels_to_trace run-k1   # or whichever you changed
make -C kernels_to_trace smoke    # quick sanity run of all programs
```

---

## Removing A Program

1. Delete the `.cu` file: `rm kernels_to_trace/kN.cu`
2. Remove `$(BUILD_DIR)/kN` from `PROGRAMS` in the Makefile.
3. Remove the `run-kN` target and its `.PHONY` entry.
4. Remove `kN` from the `smoke` target.
5. Remove the section from this README.
6. Tell the eBPF team — if any test in `daemon/ebpf/uprobes/test/` used that
   program as a trigger input, they need to update or replace it.

---

## Current Programs

### k1.cu → build/k1

CUDA Runtime API program. Allocates device memory, copies a float array to the
GPU, launches `scale_add_kernel` in a loop, synchronizes after each launch,
copies back, and frees.

Useful for triggering:

```text
cudaLaunchKernel (via <<<>>> syntax)
cudaMalloc / cudaFree
cudaMemcpy
cudaDeviceSynchronize
basic process/thread identity
```

### k2.cu → build/k2

CUDA Driver API program. Calls driver-level functions directly so uprobe
behavior is easier to reason about than with the Runtime API.

Useful for triggering:

```text
cuDevicePrimaryCtxRetain
cuCtxSetCurrent
cuMemAlloc / cuMemFree
cuMemcpyHtoDAsync
cuStreamSynchronize
cuStreamCreate / cuStreamDestroy
```

### k8.cu → build/k8

CUDA Runtime API program. Creates `NUM_STREAMS` (4) independent non-blocking
streams, launches a kernel on each stream every iteration without cross-stream
barriers, then synchronizes each stream in order before sleeping.

Useful for triggering:

```text
cudaStreamCreateWithFlags / cudaStreamDestroy
cudaLaunchKernel (multiple concurrent non-default stream args)
cudaStreamSynchronize (per-stream, not device-wide)
cudaMalloc / cudaFree (one allocation per stream)
```

---

## Planned Programs

```text
k3.cu  transfer-heavy H2D/D2H/D2D program
k4.cu  stream synchronization / wait-time program
k5.cu  multi-threaded CUDA host program
k6.cu  managed memory / UVM behavior program
```
