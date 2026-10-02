# Wedjat Daemon — Architecture

This document covers the full architecture of `wedjatd`: what it monitors, how
telemetry travels from the hardware to persistent storage, and how its internal
components are structured.

---

## 1. There Is No "Pure GPU Process"

On Linux, **every process is a CPU process**. A GPU is an accelerator connected
over PCIe; it cannot run OS processes on its own. Every machine has exactly two
kinds of CPU processes:

```
                       ┌──────────────────────────────────────────┐
                       │           Linux CPU Processes            │
                       └────────────────────┬─────────────────────┘
                                            │
               ┌────────────────────────────┴──────────────────────────┐
               ▼                                                       ▼
   TYPE A: Pure CPU Processes                            TYPE B: GPU-Accelerated Processes
   (bash, nginx, sshd, htop)                             (python3, vllm, llama-server, C++ CUDA)
   • Runs 100% on CPU cores                              • CPU host thread executes logic
   • Never touches libcuda.so                            • Calls libcuda.so to launch kernels
   • eBPF CUDA probes IGNORE these                       • eBPF CUDA probes INTERCEPT these
```

**Type A** (`bash`, `nginx`, `sshd`) never open a GPU device node or call any
CUDA function. The eBPF probes ignore them entirely.

**Type B** (`python3`, `vLLM`, `llama-server`) are standard CPU executables that
load CUDA code. The CPU host thread acts as a dispatcher — it allocates GPU
memory, copies tensors over PCIe, and submits kernels (math instructions) to the
GPU. The eBPF probes intercept every one of these calls at the exact microsecond
the CPU thread issues it.

---

## 2. The Five Telemetry Layers

### Layer 1 — Host Process & Kernel Context (eBPF Built-ins)

Every eBPF probe fires inside the kernel. The kernel provides this metadata for
free at every probe site:

- **`pid` / `tid`** — the process ID and thread ID making the GPU request
- **`comm`** — the 16-character executable name (`python3`, `llama.cpp`)
- **`cgroup_id`** — 64-bit identifier mapping the process to a Kubernetes pod or Docker container
- **`smp_processor_id`** — the host CPU core executing the thread
- **`timestamp_ns`** — boot-relative nanosecond timestamp used to align CPU and GPU events

### Layer 2 — User-Space CUDA Intent (Uprobes on `libcuda.so`)

Captures what the application *asks* the GPU to do, before the command reaches
the driver:

- **Kernel launches (`cuLaunchKernel`)** — kernel function address, block
  dimensions (`blockDim.{x,y,z}`), grid dimensions (`gridDim.{x,y,z}`),
  shared-memory size, and CUDA stream ID
- **Memory allocations (`cuMemAlloc` / `cudaMalloc`)** — requested VRAM size in
  bytes (entry probe) and the returned device pointer (exit `uretprobe`)
- **Data transfers (`cuMemcpy` / `cuMemcpyAsync`)** — transfer size in bytes and
  direction (host-to-device or device-to-host)
- **CPU stalls (`cuStreamSynchronize`)** — exact microsecond duration a CPU
  thread blocked waiting on the GPU's command queue, measured between entry and
  exit probes

### Layer 3 — OS & Driver Execution (Kprobes & Tracepoints)

Watches the proprietary NVIDIA driver (`nvidia.ko` and `nvidia-uvm.ko`) manage
the hardware:

- **IOCTL commands (`nvidia_unlocked_ioctl`)** — raw driver commands from user
  space to the GPU, including the specific command code
- **Memory mapping (`nvidia_mmap`)** — offsets and sizes when a process maps GPU
  memory into its address space
- **Interrupt latency (`nvidia_isr` & `nvidia_isr_kthread_bh`)** — microsecond
  latency between a hardware interrupt firing and the kernel thread that handles it
- **Driver errors (`nvidia_dev_xid`)** — critical hardware/driver faults (Xid
  error codes) at the moment they occur
- **CPU interference (`sched_switch` & `NET_RX`)** — scheduler and network
  softirq tracepoints that expose whether the GPU feed is stalling because a host
  thread was preempted by network traffic or disk I/O

### Layer 4 — Hardware Physical State (NVML Polling)

Because eBPF runs in the kernel, a sidecar poller queries NVML (NVIDIA
Management Library) for the physical state of the silicon:

- **Per-process accounting** — percentage of time a PID kept the GPU computing,
  and its peak VRAM usage in bytes
- **Global health** — real-time power draw (W), temperature (°C), SM clock
  speeds, memory-bandwidth utilization, and active PCIe transfer rates
- **Distributed networking (NCCL)** — for multi-GPU setups, tracking NCCL APIs
  reveals synchronization barriers and straggler nodes during `AllReduce` /
  `AllGather`

### Layer 5 — Device-Resident Micro-Telemetry (eGPU / bpftime)

Only available with dynamic PTX injection — compiling eBPF bytecode to run
*inside* the GPU's Streaming Multiprocessors:

- **Hardware placement (`%smid` & `%ctaid`)** — exactly which SM (0–131) and
  which CTA executed the code; enables an SM load-distribution heatmap
- **Warp divergence** — detects when threads within a 32-thread warp take
  different branch paths, forcing serialized execution
- **Memory coalescing (`LDG` / `STG`)** — hooks global load/store instructions
  to capture exact memory addresses and access sizes, catching uncoalesced reads
- **Instruction stalls** — nanosecond-granularity telemetry on *why* a warp is
  paused (shared memory, register dependency, or instruction fetch)

---

## 3. Full System Architecture

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER SPACE (CPU Application / Kubeflow Pod)                                │
│                                                                             │
│   GPU Process: python3 / vLLM / llama-server (PID 8842, TID 8843)           │
│     │                                                                       │
│     │ 1. Calls cuLaunchKernel(), cuMemAlloc(), cuMemcpyAsync()              │
│     ▼                                                                       │
│   ┌──────────────────────────┐                                              │
│   │        libcuda.so        │ ◄── [uprobes / uretprobes attached here]     │
│   └────────────┬─────────────┘                                              │
└────────────────│────────────────────────────────────────────────────────────┘
                 │
                 ├───────────────────► LAYER 1: Host Identity (eBPF Context)
                 │                     • PID: 8842, TID: 8843, comm: "python3"
                 │                     • K8s cgroup_id (Pod attribution)
                 │                     • Host CPU Core ID & Boot Timestamp (ns)
                 │
                 ├───────────────────► LAYER 2: CUDA Intent & API Parameters
                 │                     • cuLaunchKernel: gridDim, blockDim, sharedMem
                 │                     • cuMemAlloc: Requested bytes, devPtr, latency
                 │                     • cuMemcpyAsync: MB/s, Direction (H2D / D2H)
                 │                     • cuStreamSynchronize: CPU Stall Duration (µs)
                 │                     • cuCtxSetCurrent: Context-to-GPU ID mapping
                 ▼ (ioctl)
┌─────────────────────────────────────────────────────────────────────────────┐
│ KERNEL SPACE (Linux Kernel & GPU Drivers)                                   │
│                                                                             │
│   ┌──────────────────────────┐                                              │
│   │ nvidia.ko / nvidia-uvm   │ ◄── [kprobes / tracepoints attached here]    │
│   └────────────┬─────────────┘                                              │
│                │                                                            │
│                └───────────────────► LAYER 3: Driver & OS Subsystem State   │
│                                      • UVM Page Faults (Virtual Addr, Thrashing)│
│                                      • VRAM Page Evictions (Noisy Neighbors) │
│                                      • sched_switch: CPU Preemption & Noise │
│                                      • softirq: Network (NET_RX) / Disk Stalls│
└────────────────│────────────────────────────────────────────────────────────┘
                 │ (PCIe Bus Submission)
                 ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ PHYSICAL GPU HARDWARE & SILICON                                             │
│                                                                             │
│   ┌──────────────────────────┐                                              │
│   │ Physical GPU Hardware    │ ◄── [NVML Poller & Event Waiter (Layer 4)]   │
│   │ (RTX 3050, H100, etc.)   │     • Compute Util %, VRAM Used, Temp °C,   │
│   └────────────┬─────────────┘       Power (W), Clock MHz, Xid Fault Codes  │
│                │                                                            │
│                └───────────────────► LAYER 5: On-Device Silicon Execution   │
│                                      (Optional: eGPU / bpftime PTX JIT)     │
│                                      • %smid (Physical SM ID Heatmap)       │
│                                      • %ctaid (Block ID) & %laneid (Warp)   │
│                                      • Instruction Memory Coalescing & GPRs │
└─────────────────────────────────────────────────────────────────────────────┘
                                       │
                                       ▼ (1s Ticker / Async RingBuf Drain)
┌─────────────────────────────────────────────────────────────────────────────┐
│ wedjatd DAEMON (Go Background Service, systemd, Root / CAP_BPF)             │
│                                                                             │
│  • Reads BPF Per-CPU Map Deltas (Fast Path) & Drains RingBuffer (Errors/Stalls)│
│  • Polls NVML Stats & Executes Blocking nvmlEventSetWait for Xid Errors    │
│  • Resolves raw kernel addresses (func) to symbol names via /proc/pid/exe   │
│  • Computes UTC Anchor: (CLOCK_REALTIME - CLOCK_BOOTTIME)                  │
└──────────────────────┬──────────────────────────────────┬───────────────────┘
                        │                                  │
                        ▼ Writes                           ▼ Pushes
┌─────────────────────────────────────────┐    ┌──────────────────────────────┐
│ DISK STORAGE (/var/lib/wedjat)          │    │ UNIX DOMAIN SOCKET           │
│                                         │    │ /run/wedjat/wedjat.sock      │
│ • meta.db (SQLite WAL Mode)             │    │ (Live in-progress minute)    │
│   gpus, procs, proc_gpu, incidents      │    └──────────────┬───────────────┘
│ • YYYY-MM-DD.db (SQLite WAL Mode)       │                   │
│   gpu_samples, agg, kernel_events       │                   │ Reads (No Root)
│   1-minute aggregated time-series rows  │                   │
└──────────────────────┬──────────────────┘                   │
                       │                                      │
                       └──────────────────┬───────────────────┘
                                          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ wedjat CLI (Go TUI Client, Non-Root User)                                   │
│                                                                             │
│   • `wedjat top`     : Live process table from the Unix socket              │
│   • `wedjat live`    : Tail the live event stream from the Unix socket      │
│   • `wedjat history` : Query SQLite directly for past timelines             │
│                         (works with the daemon stopped)                     │
└─────────────────────────────────────────────────────────────────────────────┘
```

The daemon is the sole writer. The CLI is a read-only client of both consumers.

**Three architectural properties this diagram encodes:**

1. **Process boundary** — every CUDA operation originates as a standard Linux
   CPU thread issuing calls to `libcuda.so`. There is no other entry point.
2. **Fast-path aggregation** — high-frequency calls (`cuLaunchKernel`,
   `cuMemcpyAsync`) increment `BPF_MAP_TYPE_PERCPU_HASH` counters in kernel
   space with no context switch to user space.
3. **Slow-path ring buffer** — only rare events (errors, CPU stalls > 10 ms,
   process exits) flow through `BPF_MAP_TYPE_RINGBUF`, keeping ring-buffer
   traffic and disk I/O low.

---

## 4. The Telemetry Pipeline

```
sources                store layer                    consumers
───────                ───────────                    ─────────
NVML  ─────────────┐
eBPF  ─────────────┼──→ [Store] ──→ SQLite (disk) ───→ TUI reads history
proc  ─────────────┘         │                          (mode=ro, daemon can be dead)
                             │
                             └──→ unix socket ────────→ TUI reads live
                                  (mandatory)            (current minute only)
```

Two consumers, one writer.

- **SQLite** is the durable record. History is readable even when the daemon is
  not running.
- **The Unix socket** carries the one class of data that exists nowhere else:
  the **in-progress minute**. The collector's accumulator lives in daemon memory
  and is destroyed at each flush. The newest database row is up to 60 seconds
  stale; the socket is the only path to a live "current GPU state".

| Question | Source |
| :--- | :--- |
| "What did my job do at 03:00 last night?" | SQLite |
| "What is the GPU doing right now?" | Unix socket |
| "Did that 200 ms kernel ever run?" | SQLite, via `kernel_events` |

### The store is a faithful sink

The store does not filter, sample, or discard. Every field it is handed is
persisted verbatim inside a single transaction. Decisions about what to collect
belong to the collector, not the store.

---

## 5. Internal Components

```mermaid
graph TD
    A[wedjatd main] --> B[Acquire process lock]
    B --> C[Load configuration]
    C --> D[Open SQLite databases]
    D --> E[Initialize NVML and discover GPUs]
    E --> F[Start Unix socket server]
    F --> G[Start eBPF tracer when available]
    G --> H[Poll and record snapshots]
    H --> I[Heartbeat and tracer statistics]
```

| Package | Responsibility |
| :--- | :--- |
| `core/db` | Metadata and daily SQLite databases; process and GPU identity; aggregation; incidents; retention |
| `core/nvml` | CGO bridge to NVML; GPU and process polling API |
| `core/ebpf` | BPF object loading and attachment; event consumption; counter draining |
| `core` | Snapshot construction; database recording; configuration; Unix socket server |

---

## 6. Startup Sequence

1. Acquire the exclusive process lock.
2. Load the configuration file; apply defaults for any field not set.
3. Open `meta.db` and the current UTC daily database.
4. Read the boot ID. Report an unclean previous shutdown and close processes
   left open by an earlier boot.
5. Record the current boot ID and mark the run as unclean until shutdown
   completes cleanly.
6. Start the daily database retention timer.
7. Initialize NVML and register all discovered GPU identities.
8. Start the Unix socket server. Clients receive the latest cached snapshot as
   JSON.
9. Attempt to start the eBPF tracer. Failure is logged; NVML polling continues
   regardless.
10. Poll GPU and process state every 2 seconds; write heartbeats every 10 seconds.

---

## 7. Shutdown Sequence

On `SIGINT` or `SIGTERM`:

1. Mark the run as a clean shutdown in `daemon_state`.
2. Stop the polling and heartbeat loops.
3. Stop the eBPF session and NVML sources.
4. Stop the Unix socket server and remove its socket file.
5. Stop the daily retention timer; checkpoint and close SQLite.
6. Release the process lock.

---

## 8. Storage Layout

### Database split

| File | Contents | Lifetime |
| :--- | :--- | :--- |
| `meta.db` | `gpus`, `procs`, `proc_gpu`, `incidents`, `daemon_state` | Persistent |
| `YYYY-MM-DD.db` | `gpu_samples`, `agg` (time-series) | One UTC day, rotated at midnight |

The split keeps identity queries off the hot time-series file and lets retention
delete a day by removing a single file. A "yesterday" query opens exactly one
daily file.

### Attribution model

Per-minute rows reference stable ledger keys, never raw OS identifiers:

| Column | References | Registered by |
| :--- | :--- | :--- |
| `gpu_samples.gpu_id` | `gpus.gpu_id` | `UpsertGPU` at daemon start |
| `agg.gpu_id` | `gpus.gpu_id` | same |
| `agg.proc_id` | `procs.proc_id` | `UpsertProcess` on first sight |

A PID is not an identity — the kernel recycles PIDs. Identity is
`(boot_id, tgid, start_ticks)`, where `start_ticks` is field 22 of
`/proc/<pid>/stat`: the process start time in clock ticks since boot, unique
across every execution. The collector caches the resolved `proc_id` per PID and
revalidates it against `start_ticks` on each poll, so a reused PID is detected
and re-resolved rather than inheriting the previous process's row.

Both `gpus.gpu_id = 0` and `procs.proc_id = 0` are reserved sentinels seeded at
store open. Real hardware and real processes always receive IDs above `0`.

### Liveness detection

`daemon_state.heartbeat_ts` in `meta.db` is the daemon's liveness marker. The
collector beats every 10 seconds. A client must treat a heartbeat older than a
small multiple of that interval as "daemon not running" and fall back to
SQLite-only reads.

---

## 9. Data Retention Tiers

Not every signal deserves the same storage cost. Three tiers, cheapest first:

| Tier | Table | Content | Cost | Why |
| :--- | :--- | :--- | :--- | :--- |
| 1. Process ledger | `procs` | who ran, command, start/end, exit code | Cheap, always on | Identity — makes every later row attributable |
| 2. Minute aggregates | `gpu_samples`, `agg` | board telemetry + per-process counters | ~30 rows/hour | Answers "was the GPU busy, and by whom" |
| 3. Event log | `kernel_events` | kernel name, grid/block, duration | One row per launch | The only tier that captures a sub-second kernel |

**Tier 2 cannot answer "did a 220 ms job run?"** A tiled matrix multiply launches
kernels lasting single-digit milliseconds; any poll-based sampler at any sane
interval will miss them. Tier 3 must be event-driven. This is a property of the
workload, not a tuning problem.

---

## 10. A Single Event Walkthrough

The eBPF probes attach to `libcuda.so` on the host CPU. Whenever a Type B
process makes a CUDA call, eBPF intercepts it in the CPU before it ever reaches
the GPU:

```
  GPU Process (python3 / vLLM)
           │
           │  1. Calls cuLaunchKernel()
           ▼
   ┌─────────────────┐
   │   libcuda.so    │  ◄─── [eBPF Probe Fires Here on CPU]
   └────────┬────────┘
            │
            ├──────► LAYER 1 (Host Identity):
            │        • PID / TID      : 8842 / 8843
            │        • Process Name   : "python3"
            │        • Container ID   : cgroup_id (e.g., K8s pod "vllm-pod-1")
            │        • CPU Core       : Core 4
            │
            ├──────► LAYER 2 (GPU Intent & Workload Parameters):
            │        • cuLaunchKernel : gridDim (4096 blocks), blockDim (256 threads)
            │        • cuMemAlloc     : VRAM requested (e.g., 4.2 GB)
            │        • cuMemcpyAsync  : PCIe Transfer (e.g., 500 MB Host-to-Device)
            │        • cuStreamSync   : CPU Stall Duration (e.g., CPU waited 42 ms on GPU)
            │
            ▼  2. Submitted over PCIe
   ┌─────────────────┐
   │  NVIDIA Driver  │ ──► GPU Hardware (Executes Math)
   └─────────────────┘
```

What gets recorded for a running LLM server:

- `cuLaunchKernel` → `python3` (PID 8842, pod `llm-serve`) launched a kernel on GPU 0 with 4,096 blocks × 256 threads.
- `cuMemAlloc` → `vllm` (PID 9107, pod `vllm-worker`) allocated 2.1 GB of VRAM on GPU 1.
- `cuMemcpyAsync` → `llama-server` (PID 3412) pushed 512 MB of weights over PCIe from CPU RAM to GPU VRAM.
- `cuStreamSynchronize` → `python3` (PID 8842) stalled its host CPU thread for 42 ms waiting for the GPU queue to drain.

---

## 11. Background: The NVIDIA Linux Driver Stack

**Open vs. closed source.** NVIDIA released `nvidia.ko`, `nvidia-uvm.ko`,
`nvidia-drm.ko`, and `nvidia-modeset.ko` as dual-licensed (GPL/MIT) source with
the R515 driver in May 2022. The R560 line made them the default for Turing and
newer (Ampere, Ada Lovelace, Hopper); Grace Hopper and Blackwell require them
outright; Maxwell/Pascal/Volta still need the proprietary modules.

**What is open is not the whole stack.** Each kernel module splits into an
OS-agnostic core and a Linux-specific kernel interface layer. The interface layer
is compiled from source against the running kernel, but the OS-agnostic core
(e.g. the `nv-kernel.o_binary` blob inside `nvidia.ko`) ships prebuilt. The
user-space CUDA, OpenGL, and Vulkan libraries and the GSP firmware remain fully
proprietary.

**NVML.** The C library underlying `nvidia-smi`. It ships with the display driver
and exposes per-process compute lists (with memory footprint), GPU and memory
utilization percentages, ECC error counts, clock speeds, temperature, and power
draw.

**Xid errors.** The driver's hardware fault channel. The `NVRM` kernel module
prints Xid codes straight to the kernel ring buffer (`dmesg | grep -i xid`).
Common codes: Xid 79 (GPU fallen off the bus — device-wide fault), Xid 31
(memory page fault, contained to one process), Xid 94/95 (contained/uncontained
ECC errors).

### References

- [NVIDIA — Transitioning Fully Towards Open-Source GPU Kernel Modules](https://developer.nvidia.com/blog/nvidia-transitions-fully-towards-open-source-gpu-kernel-modules.md/)
- [NVIDIA/open-gpu-kernel-modules (GitHub)](https://github.com/nvidia/open-gpu-kernel-modules)
- [NVIDIA Management Library (NVML) — Developer Page](https://developer.nvidia.com/management-library-nvml)
- [Netdata — Understanding NVIDIA GPU Xid Errors](https://www.netdata.cloud/guides/nvidia-gpu/nvidia-gpu-xid-errors/)
