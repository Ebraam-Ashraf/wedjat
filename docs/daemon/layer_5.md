# Wedjat Layer 5: Device-Resident Execution & On-Chip Micro-Telemetry

Layer 5 crosses the PCIe bus boundary and executes eBPF bytecode **directly inside the physical GPU silicon** (Streaming Multiprocessors / SMs), alongside active CUDA warps. Where Layers 1–4 sit on the host CPU and kernel driver watching software requests and macro board stats, Layer 5 reaches inside the running compute binary to inspect hardware registers, warp execution state, and instruction-level memory patterns.

---

## 1. How Layer 5 Works: PTX JIT Offloading & Dynamic Instrumentation

GPU hardware doesn't natively understand 64-bit RISC eBPF instructions, so a user-space JIT translation engine — **`eGPU`** or **`bpftime`** — translates standard eBPF bytecode into NVIDIA PTX (Parallel Thread Execution) assembly, or SPIR-V, at runtime. Using dynamic binary patching, the resulting PTX snippets are injected as "trampoline" hooks into the target CUDA compute binary in VRAM, without recompiling the application or touching its source.

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ HOST CPU (User Space / Control Plane)                                       │
│                                                                             │
│  1. Compile C eBPF Program ──► Standard eBPF Bytecode (.bpf.o)              │
│  2. JIT Translation Engine  ──► Translates eBPF to NVIDIA PTX Assembly      │
│  3. Dynamic Binary Patching ──► Injects PTX Trampolines into target cubin   │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼ (Submitted over PCIe)
┌─────────────────────────────────────────────────────────────────────────────┐
│ GPU SILICON (Streaming Multiprocessors / SM #0 to SM #131)                   │
│                                                                             │
│   CUDA Compute Kernel (e.g., GEMM / Attention)                              │
│   ┌──────────────────────────────────────────────────────────────────────┐  │
│   │ 32-Thread Warp Execution                                             │  │
│   │                                                                      │  │
│   │  [Injected PTX Trampoline]                                           │  │
│   │  ├─ Read %smid, %ctaid, %tid, %laneid                                │  │
│   │  ├─ Compute Warp-Leader Aggregation (Lane 0)                         │  │
│   │  └─ Write to Shared Host-GPU BPF Map                                 │  │
│   │                                                                      │  │
│   │  [Original Math Workload (SASS Instructions)]                        │  │
│   │  └─ Floating Point Matrix Operations (FFMA, Tensor Cores)            │  │
│   └──────────────────────────────────┬───────────────────────────────────┘  │
└──────────────────────────────────────│──────────────────────────────────────┘
                                       │
                                       ▼ (Asynchronous PCIe Flush / Atomic Sync)
┌─────────────────────────────────────────────────────────────────────────────┐
│ HIERARCHICAL CROSS-LAYER BPF MAPS                                           │
│  • GPU On-Chip Shared Memory ──► Pinned Host DRAM ──► wedjatd Daemon        │
└─────────────────────────────────────────────────────────────────────────────┘
```

A second, slightly plainer pass on the same diagram — kept here too since it labels the DMA sync step differently:

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ HOST CPU (User Space / Control Plane)                                       │
│                                                                             │
│  1. Compile C eBPF Program ──► Standard eBPF Bytecode (.bpf.o)              │
│  2. JIT Translation Engine  ──► Translates eBPF to NVIDIA PTX Assembly      │
│  3. Dynamic Binary Patching ──► Injects PTX Trampolines into target binary  │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼ (Submitted over PCIe)
┌─────────────────────────────────────────────────────────────────────────────┐
│ GPU SILICON (Streaming Multiprocessors / SMs)                               │
│                                                                             │
│   CUDA Compute Kernel (e.g., Matrix Multiplication)                         │
│   ┌──────────────────────────────────────────────────────────────────────┐  │
│   │ 32-Thread Warp Execution                                             │  │
│   │                                                                      │  │
│   │  [Injected PTX Trampoline]                                           │  │
│   │  ├─ Read block indices, thread indices, global timers                │  │
│   │  ├─ Compute Warp-Leader Aggregation (Lane 0)                         │  │
│   │  └─ Write to Shared Host-GPU BPF Map                                 │  │
│   │                                                                      │  │
│   │  [Original Math Workload]                                            │  │
│   │  └─ Floating Point Operations                                        │  │
│   └──────────────────────────────────┬───────────────────────────────────┘  │
└──────────────────────────────────────│──────────────────────────────────────┘
                                       │
                                       ▼ (DMA / PCIe Sync)
┌─────────────────────────────────────────────────────────────────────────────┐
│ HIERARCHICAL CROSS-LAYER BPF MAPS                                           │
│  • GPU On-Chip Shared Memory ──► Host Pinned Memory ──► BPF Maps            │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Telemetry Inventory

By executing directly on the Streaming Multiprocessors, Layer 5 extracts intra-device micro-telemetry that CPU-side tools structurally cannot see:

1. **Physical SM & block placement (`%smid` & `%ctaid`)** — reads the special PTX hardware registers `%smid` (physical SM ID, e.g. SM 0 through SM 131) and `%ctaid.{x,y,z}` (Cooperative Thread Array / block ID), plus `%tid` and thread-level timing. This renders live SM heatmaps showing exact block distribution and flags hardware load imbalance across the chip — including cases where an execution histogram shows one thread lane sitting idle while its neighbors stay saturated (a grid-stride loop imbalance).
2. **Instruction-level memory access (`LDG`/`STG`, `LDS`/`STS`)** — intercepts global memory reads/writes and shared-memory operations to identify uncoalesced memory access (threads requesting scattered addresses over PCIe/HBM) and shared-memory bank conflicts.
3. **Warp divergence tracing** — measures SIMT branch divergence, where an `if`/`else` inside a 32-thread warp forces some lanes onto a different, serialized path. As a concrete example: in one matmul kernel, an eGPU probe found lane 31 finishing consistently ~750 ns later than lanes 0–30 because an unaligned boundary check forced it onto its own branch.
4. **Register inspection & SASS instruction stalls** — inspects general-purpose registers (`R0`–`R255`), predicate registers, and global timer ticks (`bpf_get_globaltimer()`) to expose *why* a warp is paused: shared-memory wait, register dependency, or instruction fetch.

---

## 3. The SIMT Challenge & the Warp-Leader Protocol

GPUs execute instructions in parallel units of 32 threads (warps). Naively running eBPF logic on every individual thread causes severe branch divergence, register starvation, and memory-bus contention. To run eBPF on GPUs without hanging the hardware, both `eGPU` and `bpftime` enforce a **warp-leader execution protocol**:

```text
 32-Thread Warp (Lane 0 to Lane 31)
 ┌───┬───┬───┬───┬───┬───┬─────────┬────┐
 │ 0 │ 1 │ 2 │ 3 │ 4 │ 5 │  . . .  │ 31 │
 └───┴───┴───┴───┴───┴───┴─────────┴────┘
   │   │   │   │   │   │           │
   └───┴───┴───┴───┴───┴───────────┘
                 │
                 ▼ Intra-Warp Reduction (__shfl_sync)
 ┌────────────────────────────────────────────────────────┐
 │ Lane 0 (Warp Leader: ThreadIdx.x % 32 == 0)            │
 │  1. Collects aggregated thread values                  │
 │  2. Executes eBPF probe logic ONCE for the warp        │
 │  3. Updates shared BPF map in VRAM                     │
 └────────────────────────────────────────────────────────┘
```

1. **Uniformity verification** — a static verifier separates program state into *lane-varying* values (thread-local IDs) and *warp-uniform* values (block IDs, timer ticks), and requires that control-flow branch decisions depend only on the warp-uniform ones.
2. **Warp-leader execution** — individual lanes compute local metric contributions, aggregate them to lane 0 via intra-warp reduction primitives (`__shfl_sync`), and lane 0 runs the actual probe handler once for the whole warp, then broadcasts any decision back.

This warp-uniform model is the specific thing that keeps overhead low — see the numbers in §5.

---

## 4. Code: `ebpf/gpu/gpu_sm.bpf.c`

A device-resident eBPF snippet, compiled for GPU execution, hooking a kernel entry point and using GPU-specific helpers (`bpf_get_globaltimer()`, inline PTX for `%smid`):

```c
#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "common.h"

char LICENSE[] SEC("license") = "Dual BSD/GPL";

// Device GPU BPF Map (Resides in Host-GPU Shared VRAM)
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 132); // Max 132 SMs
    __type(key, u32);         // Key = Physical SM ID (%smid)
    __type(value, u64);       // Value = Total execution time / block count
} sm_heatmap_map SEC(".maps");

// Device-resident Kprobe attached to a CUDA kernel entry
SEC("kprobe/matmul_kernel")
int BPF_KPROBE(probe_gpu_matmul_entry)
{
    // 1. Read GPU Global Nanosecond Timer
    u64 ts_gpu = bpf_get_globaltimer();

    // 2. Read Physical SM ID via PTX Register (%smid)
    u32 sm_id = 0;
    asm volatile("mov.u32 %0, %%smid;" : "=r"(sm_id));

    // 3. Update SM execution count map in VRAM (Executed by Warp Leader)
    u64 *val = bpf_map_lookup_elem(&sm_heatmap_map, &sm_id);
    if (val) {
        __sync_fetch_and_add(val, 1);
    }

    return 0;
}
```

---

## 5. Overhead: Why Layer 5 Stays Opt-In

The headline overhead numbers here — device-side eBPF landing at roughly 3–14% versus 85–93% for binary rewriters like NVBit — come from a real, checkable benchmark, not a made-up figure: the `gpu_ext` paper's device-observability evaluation (llama.cpp prefill, Llama 1B, on an NVIDIA P40) measured `kernelretsnoop` at 8%, `threadhist` at 3%, and `launchlate` at 14%, against 85%, 87%, and 93% for the equivalent NVBit-based tools — a 3–10x reduction attributed specifically to warp-uniform execution avoiding divergent control flow and redundant memory access. The same paper puts *host*-side `gpu_ext` runtime overhead (hooks enabled, no policy attached) under 0.2% on an RTX 5090, which is the real-world anchor for the "always-on daemon" overhead figure below.

| Metric / Dimension | Layers 1–4 (Host eBPF + NVML) | Layer 5 (On-Device PTX JIT) |
| :--- | :--- | :--- |
| **Execution Domain** | Host CPU Kernel / Driver / User Space | **GPU Silicon (Streaming Multiprocessors)** |
| **Primary Mechanism** | `uprobes` on `libcuda.so`, `kprobes`, NVML Polling | **PTX JIT Translation & Trampoline Injection (`eGPU`/`bpftime`)** |
| **Telemetry Captured** | PIDs, `cuLaunchKernel` dims, VRAM allocs, CPU stalls, Temp, Power, Xid Errors | **Physical `%smid` SM ID, `%ctaid` Block ID, `LDG`/`STG` memory ops, Warp Divergence** |
| **Overhead** | Host-side hooks with no policy attached: well under 1% (published `gpu_ext` figure: <0.2%) | Device-side observability tools: ~3–14% (vs. 85–93% for NVBit-class binary rewriting) |
| **Role in `wedjat`** | **`wedjatd` Continuous Daemon** | **Phase 2 On-Demand Deep Profiler** |

Given that spread, build `wedjatd`'s always-on path around Layers 1–4, and keep Layer 5 (`ebpf/gpu/gpu_sm.bpf.c`) as a specialized, on-demand deep-profiling mode for physical SM distribution or instruction-level memory data. It should not run continuously against production workloads.

### Sources

- [eGPU: Extending eBPF Programmability and Observability to GPUs — ACM HCDS '25](https://doi.org/10.1145/3723851.3726984)
- [gpu_ext: Extensible OS Policies for GPUs via eBPF (arXiv:2512.12615)](https://arxiv.org/abs/2512.12615)
- [bpftime — GPU support (eunomia-bpf, GitHub)](https://github.com/eunomia-bpf/bpftime/tree/master/example/gpu)