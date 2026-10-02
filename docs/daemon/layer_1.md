# Layer 1 — Host Process Context & Thread-to-Device Mapping

## Overview

Layer 1 is the foundational telemetry layer of Wedjat's eBPF pipeline. It runs inside the Linux kernel every time any higher-layer probe fires — uprobes on `libcuda.so`, kprobes on `nvidia-uvm.ko`, or a system tracepoint — and answers the question every other layer depends on: **which process, thread, container, CPU core, and (on multi-GPU hosts) which physical device** issued a given GPU request. Without it, GPU events are anonymous: you'd see that a `cuLaunchKernel` fired, but not who fired it, from which pod, or when in wall-clock time.

---

## Why Hook `libcuda.so`, Not `libcudart.so`

Probes attach to `libcuda.so` (the CUDA **Driver** API) rather than `libcudart.so` (the CUDA **Runtime** API). Heavy AI frameworks — PyTorch, vLLM, cuBLAS, `torch.compile` — frequently link `libcudart` statically or bypass the runtime wrapper entirely to call driver entry points directly. Attaching to `libcuda.so` guarantees that every CUDA application on the system is intercepted, regardless of how it was compiled.

## How the Uprobe Hook Works

1. **Trigger** — a CPU thread in a GPU-accelerated process calls a function such as `cuLaunchKernel` or `cuMemAlloc`, and execution reaches the entry boundary inside `libcuda.so`.
2. **Interception (`uprobe`)** — the attached eBPF program fires at the function's entry point; execution pauses for roughly 1–2 microseconds while it runs in kernel context.
3. **Paired exit probing (`uretprobe`)** — for calls whose result is only known on return (`cuMemAlloc`'s returned `devPtr`, or `cuStreamSynchronize`'s total wait time), an entry `uprobe` stashes the start timestamp in a temporary BPF hash map (`inflight_map`), and a matching exit `uretprobe` computes the duration and reads the return code.

---

## The Six Layer 1 Primitives

Every probe invocation extracts the executing host context via six built-in eBPF kernel helpers — instant, zero-overhead queries against the currently running `task_struct`, with no I/O and no process-table search:

| # | Helper | What It Extracts | Why It Matters to Wedjat |
|---|--------|-------------------|---------------------------|
| 1 | `bpf_get_current_pid_tgid()` | A 64-bit value: upper 32 bits = Thread Group ID (host PID), lower 32 bits = Thread ID (host TID) | Uniquely identifies the host process issuing GPU commands, separating multi-threaded Python/C++ workers |
| 2 | `bpf_get_current_comm()` | The 16-byte null-terminated executable name straight from `task_struct` (e.g. `python3`, `vllm`, `ollama`) | Populates process names in `wedjat top` instantly, with no `/proc/<pid>/comm` read in user space |
| 3 | `bpf_get_current_cgroup_id()` | The 64-bit cgroup v2 ID assigned to the process | Critical for clusters — lets `wedjatd` map host PIDs directly to Docker containers, Kubernetes Pods, or Kubeflow worker ranks despite PID-namespace isolation |
| 4 | `bpf_get_current_uid_gid()` | The User ID (`uid`) and Group ID (`gid`) running the workload | Enables multi-tenant security auditing on shared GPU servers — who launched a given job |
| 5 | `bpf_get_smp_processor_id()` | The physical host CPU core executing the launch thread | Detects CPU NUMA bottlenecks and thread-migration issues (e.g. a thread on CPU socket 1 submitting to a GPU on PCIe socket 0) |
| 6 | `bpf_ktime_get_boot_ns()` | Monotonic nanoseconds since boot, including suspend time | Lets `wedjatd` convert boot timestamps to exact UTC using one boot anchor (`CLOCK_REALTIME − CLOCK_BOOTTIME`) |

### Without vs. With Layer 1

```text
WITHOUT LAYER 1 (Anonymous Raw GPU Event):
┌─────────────────────────────────────────────────────────┐
│ Event: cuLaunchKernel() executed                        │
│ Grid: (1024,1,1), Block: (256,1,1)                      │
│ ❓ Which process ran it?      UNKNOWN                   │
│ ❓ Which Kubeflow pod was it? UNKNOWN                   │
│ ❓ When did it happen in UTC? UNKNOWN                   │
└─────────────────────────────────────────────────────────┘

WITH LAYER 1 (Enriched Telemetry Envelope):
┌─────────────────────────────────────────────────────────┐
│ Event: cuLaunchKernel() executed                        │
│ Grid: (1024,1,1), Block: (256,1,1)                      │
│ 👤 Executable Name : "python3"                          │
│ 🔢 PID / TID       : 9107 / 9108                        │
│ 🐳 Container ID    : cgroup_id = 0x8a2f10b46c01         │
│ 🖥️ Host CPU Core   : Core #3                            │
│ ⏱️ Boot Time ns    : 1,824,792,100,240 ns               │
└─────────────────────────────────────────────────────────┘
```

---

## Multi-GPU Thread-to-Device Mapping

On multi-GPU hosts, Layer 1 also tracks which physical device a thread is currently bound to, so every later probe can tag its event with the right `device_id`. Two hooks maintain this:

- **`cuCtxSetCurrent(CUcontext ctx)`** — binds the calling thread to whichever GPU that CUDA context maps to.
- **`cuDevicePrimaryCtxRetain(CUcontext *pctx, unsigned int dev)`** — maps the physical GPU index (`dev`) to the calling thread directly.

These populate two small BPF hash maps, `tid_to_device` and `ctx_to_device`. Once populated, any Layer 2 or Layer 3 probe simply looks up the calling thread's ID in `tid_to_device` to tag its event with the correct physical GPU.

---

## Architecture & Execution Flow

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER SPACE (Application / Kubeflow Pod / CPU Thread)                         │
│                                                                             │
│  Process: python3 / vLLM (PID: 8842, TID: 8843)                            │
│  cgroup:  /kubeflow.slice/kubeflow-pod-123.scope                           │
│     │                                                                       │
│     │ 1. Application calls CUDA function: cuLaunchKernel(...)               │
│     ▼                                                                       │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ libcuda.so (CUDA Driver API)                                          │  │
│  │  Function Entry: cuLaunchKernel(func, gridDim, blockDim, stream, ...)  │  │
│  └───────────────────────────────────┬───────────────────────────────────┘  │
└──────────────────────────────────────│──────────────────────────────────────┘
                                       │
      ┌────────────────────────────────┘
      │ 2. Uprobe Interception (Break-instruction / Trap into BPF runtime)
      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ KERNEL SPACE (Linux Kernel & eBPF Engine)                                   │
│                                                                             │
│  SEC("uprobe/cuLaunchKernel")                                               │
│  int probe_cuLaunchKernel_entry(struct pt_regs *ctx)                        │
│  {                                                                          │
│      // A. Extract Free Process Context (Layer 1 Metadata)                  │
│      u64 pid_tgid = bpf_get_current_pid_tgid();  // PID 8842, TID 8843      │
│      u64 cgroup_id = bpf_get_current_cgroup_id(); // K8s Pod Container ID    │
│      u64 ts_ns     = bpf_ktime_get_boot_ns();   // Boot timestamp          │
│      bpf_get_current_comm(&comm, sizeof(comm));   // "python3"               │
│                                                                             │
│      // B. Extract Function Arguments off CPU Registers/Stack                │
│      u32 gridX  = PT_REGS_PARM2(ctx);  // Grid Dim X                       │
│      u32 blockX = PT_REGS_PARM3(ctx);  // Block Dim X                      │
│                                                                             │
│      // C. Multi-GPU Context Lookup                                         │
│      u32 device_id = bpf_map_lookup_elem(&tid_to_device, &tid);             │
│                                                                             │
│      // D. Fast-Path Aggregation in BPF Map                                 │
│      struct cuda_agg_key key = { pid, cgroup_id, api_id, device_id };       │
│      bpf_map_update_elem(&agg_map, &key, &val, BPF_EXIST);                  │
│  }                                                                          │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ├──────► BPF_MAP_TYPE_PERCPU_HASH (Fast-Path Aggregate)
                                       │        [key: {PID, cgroup, API_ID, GPU_ID} -> val: {counts, bytes}]
                                       │
                                       └──────► BPF_MAP_TYPE_RINGBUF (Slow-Path Events)
                                                [Emits errors or stalls > 10ms to user space]
                                       │
                                       ▼ (Polled every 1s / Drained asynchronously)
┌─────────────────────────────────────────────────────────────────────────────┐
│ wedjatd DAEMON (Go User-Space Process)                                      │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Kernel Context Extraction, End to End

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER SPACE (PyTorch, vLLM, llama.cpp, Kubeflow Pods)                        │
│                                                                             │
│  Process: "python3" (PID: 9107, TID: 9108, Container: kubeflow/vllm-node-1) │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ Calls cuLaunchKernel() / cudaMalloc()
                                       ▼
═══════════════════════════ LINUX KERNEL SPACE ════════════════════════════════
┌─────────────────────────────────────────────────────────────────────────────┐
│  eBPF Probe Triggered (uprobe / kprobe / tracepoint)                        │
│                                                                             │
│  LAYER 1: eBPF Kernel Helper Calls (Instant Kernel-State Queries)           │
│  ├── bpf_get_current_pid_tgid()  ──► Extracts Host PID (9107) & TID (9108)   │
│  ├── bpf_get_current_comm()      ──► Extracts Process Name ("python3")     │
│  ├── bpf_get_current_cgroup_id() ──► Extracts 64-bit K8s CGroup ID         │
│  ├── bpf_get_current_uid_gid()   ──► Extracts User ID (0) & Group ID (0)   │
│  ├── bpf_get_smp_processor_id()  ──► Extracts Host CPU Core ID (Core #3)   │
│  └── bpf_ktime_get_boot_ns()     ──► Captures High-Res Boot Time (ns)      │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼ Enriched Telemetry Envelope
┌─────────────────────────────────────────────────────────────────────────────┐
│ BPF MAP / RING BUFFER (Passed to wedjatd Daemon)                            │
│ { ts: 1824792000, pid: 9107, cgroup: 0x4f82a, comm: "python3", cpu: 3 ... } │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## CUDA API Function Arguments Captured Alongside Host Identity

The same `libcuda.so` probes that extract Layer 1 host identity also read function parameters directly off CPU registers/stack (`PT_REGS_PARM*`) at the same call site:

| Function Hook | Parameters Captured | Diagnostic Meaning |
| :--- | :--- | :--- |
| **`cuLaunchKernel`** | `func` address, `gridDim.{x,y,z}`, `blockDim.{x,y,z}`, `sharedMem` bytes, `stream` handle | Workload dispatch frequency, total grid/block thread geometry, stream concurrency |
| **`cuMemAlloc`** | Requested bytes, returned `devPtr` address, return status, allocation latency | Active VRAM allocation rate, memory leaks, allocation latency spikes |
| **`cuMemcpyAsync`** | Byte count, transfer direction (`HostToDevice`, `DeviceToHost`, `DeviceToDevice`), stream handle | PCIe bus transfer volume and directional memory bandwidth |
| **`cuStreamSynchronize`** | Entry/exit latency delta (`us`), `stream` handle | **CPU bottleneck signal:** exact microsecond duration a host thread was blocked waiting for the GPU |
| **`cuCtxSetCurrent`** | `ctx` handle, `TID` | Maps thread IDs to physical `device_id` values on multi-GPU servers |

---

## In-Kernel Data Path: Fast-Path vs. Slow-Path

To keep overhead low (targeting roughly under 0.4%) and avoid disk-log explosions during bursts (e.g. 10,000 kernel launches/sec):

1. **Fast-path aggregation (`BPF_MAP_TYPE_PERCPU_HASH`)** — high-frequency calls (`cuLaunchKernel`, `cuMemcpyAsync`) update per-CPU BPF map counters directly in kernel space, incrementing call counts and byte totals without generating a per-call event stream. `wedjatd` polls the map deltas once per second.
2. **Slow-path ring buffer (`BPF_MAP_TYPE_RINGBUF`)** — only rare or anomalous events (a non-zero CUDA status code, an allocation over 100 MB, a CPU stall over 10 ms) are pushed to the ring buffer for user-space logging.

---

## Implementation

### `ebpf/common.h`

Shared header included by all Layer 1–3 eBPF C programs, as well as the user-space daemon — defines map keys, values, and event structs.

```c
#ifndef __COMMON_H
#define __COMMON_H

typedef unsigned char u8;
typedef unsigned short u16;
typedef unsigned int u32;
typedef unsigned long long u64;

// Event Types for BPF_MAP_TYPE_RINGBUF (Slow path)
enum cuda_event_type {
    EVENT_MALLOC_SLOW = 1,
    EVENT_SYNC_SLOW   = 2,
    EVENT_CUDA_ERROR  = 3,
    EVENT_PROC_EXIT   = 4,
    EVENT_CTX_SET     = 5,
};

// Map Key for Aggregation Hash Map (Fast path)
struct cuda_agg_key {
    u32 pid;        // Host Process ID
    u64 cgroup_id;  // 64-bit K8s Pod / Container cgroup ID
    u32 api_id;     // 1 = Launch, 2 = Malloc, 3 = Memcpy, 4 = Sync, 5 = CtxSet
    u32 device_id;  // Physical GPU Device Index
};

// Map Value for High-Frequency Aggregates
struct cuda_agg_val {
    u64 call_count;
    u64 total_bytes;
    u64 total_latency_ns;
};

// Ring Buffer Event Struct (Slow path / Notable events)
struct cuda_event {
    u64 timestamp_ns; // Boot-relative timestamp via bpf_ktime_get_boot_ns()
    u64 cgroup_id;    // Container ID
    u32 pid;          // Process ID
    u32 tid;          // Thread ID
    u32 device_id;    // Target Physical GPU ID
    u32 event_type;   // Event category
    u32 error_code;   // CUDA error code
    u64 bytes;        // Memory bytes (alloc/transfer)
    u64 latency_us;   // CPU stall or operation latency
    char comm;    // Executable name (e.g., "python3", "vllm")
};

#endif /* __COMMON_H */
```

### `ebpf/uprobes/host_ctx.bpf.c`

The primary Layer 1 program. It does two things: (1) extracts host process metadata via the six kernel helpers above, and (2) maintains the `tid_to_device` / `ctx_to_device` mappings by hooking `cuCtxSetCurrent` and `cuDevicePrimaryCtxRetain`.

```c
#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "common.h"

char LICENSE[] SEC("license") = "Dual BSD/GPL";

// Map 1: Thread ID (TID) -> Physical GPU Device ID mapping
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key, u32);   // Key = Thread ID (tid)
    __type(value, u32); // Value = Physical GPU ID
} tid_to_device SEC(".maps");

// Map 2: CUDA Context Pointer (ctx) -> Physical GPU Device ID mapping
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key, u64);   // Key = CUcontext pointer address
    __type(value, u32); // Value = Physical GPU ID
} ctx_to_device SEC(".maps");

// Map 3: Fast-Path Per-CPU Aggregation Map
struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_HASH);
    __uint(max_entries, 10240);
    __type(key, struct cuda_agg_key);
    __type(value, struct cuda_agg_val);
} agg_map SEC(".maps");

// Helper: Record Layer 1 Host Identity & Increments Aggregation Counters
static __always_inline void record_host_identity(u32 device_id, u32 api_id) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 pid = pid_tgid >> 32;

    struct cuda_agg_key key = {
        .pid = pid,
        .cgroup_id = bpf_get_current_cgroup_id(),
        .api_id = api_id,
        .device_id = device_id
    };

    struct cuda_agg_val *val = bpf_map_lookup_elem(&agg_map, &key);
    if (val) {
        val->call_count += 1;
    } else {
        struct cuda_agg_val zero_val = { .call_count = 1, .total_bytes = 0, .total_latency_ns = 0 };
        bpf_map_update_elem(&agg_map, &key, &zero_val, BPF_NOEXIST);
    }
}

// 1. Hook: cuCtxSetCurrent(CUcontext ctx)
// Binds the calling thread (TID) to the GPU device associated with CUcontext
SEC("uprobe/cuCtxSetCurrent")
int BPF_KPROBE(probe_cuCtxSetCurrent_entry, u64 ctx)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    // Look up physical GPU ID bound to this context
    u32 *dev_ptr = bpf_map_lookup_elem(&ctx_to_device, &ctx);
    u32 device_id = dev_ptr ? *dev_ptr : 0;

    // Store thread-to-device mapping
    bpf_map_update_elem(&tid_to_device, &tid, &device_id, BPF_ANY);

    // Record host context event (API ID 5 = CtxSet)
    record_host_identity(device_id, 5);

    return 0;
}

// 2. Hook: cuDevicePrimaryCtxRetain(CUcontext *pctx, u32 dev)
// Directly maps the physical GPU device index (dev) to the calling thread
SEC("uprobe/cuDevicePrimaryCtxRetain")
int BPF_KPROBE(probe_cuDevicePrimaryCtxRetain_entry, u64 pctx, u32 dev)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    // Store thread-to-device mapping directly
    bpf_map_update_elem(&tid_to_device, &tid, &dev, BPF_ANY);

    record_host_identity(dev, 5);
    return 0;
}
```

**How it operates in practice:**

1. **Automatic context resolution** — when a process binds a thread to GPU 1 via `cuCtxSetCurrent`, `host_ctx.bpf.c` intercepts the call, reads `bpf_get_current_pid_tgid()`, and saves `tid -> device_id = 1` in `tid_to_device`.
2. **Zero-overhead reuse** — any Layer 2 or Layer 3 probe firing afterward queries `tid_to_device` by the calling thread ID to tag every kernel launch and VRAM allocation with the correct physical GPU.
3. **Container attribution** — `bpf_get_current_cgroup_id()` captures the exact Kubernetes/Kubeflow pod identifier on every execution.

### `bpf/cuda_identity.bpf.c`

A simpler, standalone example showing the six Layer 1 primitives captured into a single event struct and attached directly to a probe:

```c
#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "common.h"

char LICENSE[] SEC("license") = "Dual BSD/GPL";

// Helper function to capture Layer 1 Host Context into an event struct
static __always_inline void capture_layer1_context(struct cuda_event *e) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u64 uid_gid  = bpf_get_current_uid_gid();

    e->timestamp_ns = bpf_ktime_get_boot_ns();
    e->pid          = pid_tgid >> 32;       // Host PID
    e->tid          = (u32)pid_tgid;        // Host TID
    e->cgroup_id    = bpf_get_current_cgroup_id(); // Container ID
    e->cpu_id       = bpf_get_smp_processor_id();  // CPU Core ID
    
    // Copy 16-byte process name (comm)
    bpf_get_current_comm(&e->comm, sizeof(e->comm));
}

// Example probe utilizing Layer 1
SEC("uprobe/cuLaunchKernel")
int BPF_KPROBE(probe_cuLaunchKernel_identity, void *f) {
    struct cuda_event e = {};
    
    // Populate Layer 1 metadata
    capture_layer1_context(&e);
    
    // Event specific processing...
    return 0;
}
```

---

## Where Layer 1 Fits in the Full Architecture

| Layer | Domain | Primary Mechanism | Telemetry Captured |
| --- | --- | --- | --- |
| **Layer 1** | Host OS context | Kernel BPF helpers | PID, TID, process name, K8s cgroup ID, CPU core, boot timestamp |
| **Layer 2** | User CUDA API | `uprobes` on `libcuda.so` | `cuLaunchKernel` grid/block dims, VRAM allocs, CPU stalls |
| **Layer 3** | OS driver | `kprobes` on `nvidia-uvm.ko` | UVM page faults, PCIe page eviction, CPU context switches |
| **Layer 4** | Physical GPU | NVML C API polling | Board temp (°C), power (W), overall GPU util %, Xid faults |
| **Layer 5** | GPU silicon | PTX JIT offloading (`eGPU`) | Physical SM ID (`%smid`), block ID (`%ctaid`), warp divergence |