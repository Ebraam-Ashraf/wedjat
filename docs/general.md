# Wedjat: eBPF-Based GPU/CUDA Monitoring — Telemetry & Architecture Reference

This is the master inventory of every data point available to a comprehensive terminal-based GPU monitoring tool — a "hyper-advanced `htop` for GPUs" — built on an eBPF GPU-monitoring architecture. It covers every telemetry layer, exactly where each data point originates, and how it feeds the daemon.

---

## 1. Understanding the Process Model: There Is No "Pure GPU Process"

On Linux, **every process is a CPU process**. A GPU cannot start or run an OS process on its own — it is an accelerator connected over the PCIe bus. Any given machine only has two kinds of processes running on the CPU:

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

**Type A — Pure CPU processes** (`bash`, `nginx`, `sshd`): these only ever execute on host CPU cores and system RAM. They never open a GPU driver device node (`/dev/nvidia*`) or call a CUDA function, so the eBPF probes simply ignore them.

**Type B — GPU-accelerated processes** (`python3`, `vLLM`, `llama-server`, custom CUDA/C++): standard CPU executables that load CUDA code. The CPU thread acts as a *dispatcher* — it allocates GPU memory, copies tensors across PCIe, and sends kernels (math instructions) to the GPU. The eBPF probes intercept every one of these GPU commands at the exact microsecond the CPU thread issues it.

---

## 2. The Five Telemetry Layers

### Layer 1 — Host Process & Kernel Context (eBPF Built-ins)

Every time an eBPF probe fires, the kernel provides free metadata about the process that triggered it:

- **`pid` / `tid`** — the process ID and thread ID making the GPU request
- **`comm`** — the 16-character executable name (e.g. `python3`, `llama.cpp`)
- **`cgroup_id`** — the 64-bit identifier mapping the process to a specific Kubernetes pod or Docker container
- **`smp_processor_id`** — the specific host CPU core executing the thread
- **`timestamp_ns`** — a high-precision, boot-relative nanosecond timestamp used to align CPU and GPU events

### Layer 2 — User-Space CUDA Intent (Uprobes on `libcuda.so`)

Captures what the application *asks* the GPU to do, before the command reaches the driver:

- **Kernel launches (`cuLaunchKernel`)** — kernel function address, block dimensions (`blockDim.{x,y,z}`), grid dimensions (`gridDim.{x,y,z}`), requested shared-memory size, and the CUDA stream ID
- **Memory allocations (`cuMemAlloc` / `cudaMalloc`)** — requested VRAM size in bytes (entry probe) and the returned physical GPU memory pointer (exit `uretprobe`)
- **Data transfers (`cuMemcpy` / `cuMemcpyAsync`)** — transfer size in bytes and direction (host-to-device or device-to-host)
- **CPU stalls (`cuStreamSynchronize`)** — measuring the time between entry and exit probes on synchronization calls captures the exact microsecond duration a CPU thread stalled waiting on the GPU's queue

### Layer 3 — OS & Driver Execution (Kprobes & Tracepoints)

Watches the proprietary NVIDIA driver (`nvidia.ko` and `nvidia-uvm.ko`) manage the hardware:

- **IOCTL commands (`nvidia_unlocked_ioctl`)** — raw driver commands sent from user space to the GPU driver, including the specific command code (`arg1`)
- **Memory mapping (`nvidia_mmap`)** — memory offsets and allocation sizes when a process maps GPU memory into its own address space
- **Interrupt latency (`nvidia_isr` & `nvidia_isr_kthread_bh`)** — the microsecond latency between a hardware interrupt firing and the kernel thread that actually processes it
- **Driver errors (`nvidia_dev_xid`)** — critical hardware/driver faults (Xid error codes) the moment they occur
- **CPU interference (`sched_switch` & `NET_RX`)** — scheduler and network-softirq tracepoints that reveal whether the GPU feed is stalling because the host thread was preempted by network traffic or disk I/O

### Layer 4 — Hardware Physical State (NVML Polling)

Because eBPF runs in the kernel, a sidecar poller queries NVML (NVIDIA Management Library) for the physical realities of the silicon:

- **Per-process accounting** — the percentage of time a given PID kept the GPU computing, and its peak VRAM usage in bytes
- **Global health** — real-time power draw (W), temperature (°C), SM clock speeds, memory-bandwidth utilization, and active PCIe transfer rates
- **Distributed networking (NCCL)** — for multi-GPU setups, tracking NCCL APIs reveals synchronization barriers and identifies straggler nodes during `AllReduce` / `AllGather` operations

### Layer 5 — Device-Resident Micro-Telemetry (eGPU / bpftime)

Only unlocked with dynamic PTX injection — compiling eBPF bytecode to run *inside* the GPU's Streaming Multiprocessors — this layer exposes instruction-level hardware truths:

- **Hardware placement (`%smid` & `%ctaid`)** — exactly which Streaming Multiprocessor (SM 0–131) and which Cooperative Thread Array (block) executed the code, enabling an SM load-distribution heatmap
- **Warp divergence** — detects when threads within the same 32-thread warp take different branch paths (e.g. an `if`/`else`), forcing serialized execution and destroying performance
- **Memory coalescing (`LDG` / `STG`)** — hooks global load/store instructions to capture exact memory addresses and access sizes, catching uncoalesced reads where threads request scattered VRAM addresses
- **Instruction stalls** — nanosecond-granularity telemetry on *why* a warp is paused (waiting on shared memory, a register dependency, or an instruction fetch)

---

## 3. Full System Architecture

The diagram below shows how CPU-side processes, eBPF probes, kernel drivers, NVML, and on-device execution (Layer 5) all connect through the `wedjatd` daemon down to disk storage and the `wedjat` CLI.

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
│ DISK STORAGE (/var/lib & /var/log)      │    │ UNIX DOMAIN SOCKET           │
│                                         │    │ /run/wedjat/wedjat.sock      │
│ • wedjat.db (SQLite WAL Mode)           │    │ (Stream 1s JSON Snapshots)   │
│   1-second aggregated time-series rows  │    └──────────────┬───────────────┘
│ • events.jsonl (JSON Lines Log)         │                   │
│   Rare events (errors, stalls >10ms)    │                   │ Reads (No Root)
└──────────────────────┬──────────────────┘                   │
                       │                                      │
                       └──────────────────┬───────────────────┘
                                          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ wedjat CLI (Go TUI Client, Non-Root User)                                   │
│                                                                             │
│   • `wedjat top`     : Stream 1s live process table from Unix socket        │
│   • `wedjat live`    : Tail live event stream from Unix socket              │
│   • `wedjat history` : Query SQLite WAL directly for offline/past timelines │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Key Takeaways from the Diagram

1. **Process boundary** — every CUDA operation begins as a standard Linux CPU thread issuing calls to `libcuda.so`.
2. **Fast-path aggregation** — high-frequency API calls (`cuLaunchKernel`, `cuMemcpyAsync`) update `BPF_MAP_TYPE_PERCPU_HASH` map counters in kernel space without a context switch to user space.
3. **Slow-path ring buffer** — only rare events (errors, CPU stalls > 10 ms, process exits) stream through `BPF_MAP_TYPE_RINGBUF`, keeping disk I/O low.
4. **Decoupled architecture** — the `wedjatd` daemon handles root privileges, eBPF probes, and database writes in the background; the `wedjat` CLI runs as a non-root client, reading the database or streaming over the Unix socket.

---

## 4. Layer 1 & 2 in Detail: A Single Event Walkthrough

The eBPF probes attach to `libcuda.so` (the CUDA Driver API) on the host CPU. Whenever a Type B process makes a CUDA call, eBPF intercepts it on the CPU before it ever reaches the GPU:

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

### Concrete Example of What Gets Logged

When `wedjatd` monitors a running PyTorch model or LLM server, it combines Layer 1 and Layer 2 into readable, structured data:

- **`cuLaunchKernel`** → Process `python3` (PID 8842, pod `llm-serve`) launched a GPU kernel on GPU 0 with 4,096 blocks and 256 threads per block.
- **`cuMemAlloc`** → Process `vllm` (PID 9107, pod `vllm-worker`) allocated 2.1 GB of VRAM on GPU 1.
- **`cuMemcpyAsync`** → Process `llama-server` (PID 3412) pushed 512 MB of weights over PCIe from CPU RAM to GPU VRAM.
- **`cuStreamSynchronize`** → Process `python3` (PID 8842) had its host CPU thread stalled for 42 ms waiting for the GPU to finish its calculation.

---

## 5. Summary

- There are no "pure GPU binaries" running independently on Linux — only **CPU processes that issue commands to the GPU**.
- **Layer 1** answers **who** issued the request (`PID`, process name `comm`, Kubernetes `cgroup_id`).
- **Layer 2** answers **what** they asked the GPU to do (`gridDim`/`blockDim`, VRAM allocated, PCIe MB/s, CPU stall time).
- **Layers 3–5** fill in **how the driver, the OS, and the silicon itself** responded — from IOCTLs and interrupt latency, through NVML's view of the physical device, down to per-SM instruction-level execution.

---

## 6. Background: How NVIDIA's Linux Driver Stack Actually Works

A few grounding facts about the real driver components this architecture hooks into:

**Open vs. closed source.** NVIDIA first released `nvidia.ko`, `nvidia-drm.ko`, `nvidia-uvm.ko`, and `nvidia-modeset.ko` as dual GPL/MIT-licensed source alongside the R515 driver in May 2022, initially targeting data-center GPUs. With the R560 driver line, NVIDIA made these open kernel modules the default and recommended install path for Turing-generation GPUs and newer (Ampere, Ada Lovelace, Hopper); Grace Hopper and Blackwell platforms require them outright, while Maxwell/Pascal/Volta-era cards still need the proprietary driver.

**What's open isn't the whole stack.** Each kernel module splits into an OS-agnostic core and a Linux-specific "kernel interface layer." The interface layer is compiled from source against the running kernel, but the OS-agnostic core (e.g. the `nv-kernel.o_binary` component behind `nvidia.ko`) still ships as a prebuilt binary blob. The user-space CUDA, OpenGL, and Vulkan libraries, along with the GSP firmware, remain fully proprietary regardless of which kernel module flavor is installed.

**NVML.** The C library underlying `nvidia-smi` itself — it ships with the display driver and exposes per-process compute-process lists (with memory footprint), GPU/memory utilization percentages, ECC error counts, clock speeds, temperature, and power draw. It's widely accessed from Python via the `pynvml` wrapper.

**Xid errors.** The driver's dedicated fault channel: the `NVRM` kernel module prints an Xid code straight to the kernel ring buffer — visible via `dmesg | grep -i xid` — rather than through `nvidia-smi` or a sysfs file. Common codes include Xid 79 ("GPU has fallen off the bus," a device-wide fault usually requiring a reset), Xid 31 (a memory page fault tied to one PID/channel, contained to that process), and Xid 94/95 (contained/uncontained ECC errors).

### Sources

- [NVIDIA — Transitioning Fully Towards Open-Source GPU Kernel Modules](https://developer.nvidia.com/blog/nvidia-transitions-fully-towards-open-source-gpu-kernel-modules.md/)
- [NVIDIA/open-gpu-kernel-modules (GitHub)](https://github.com/nvidia/open-gpu-kernel-modules)
- [NVIDIA Management Library (NVML) — Developer Page](https://developer.nvidia.com/management-library-nvml)
- [Netdata — Understanding NVIDIA GPU Xid Errors](https://www.netdata.cloud/guides/nvidia-gpu/nvidia-gpu-xid-errors/)
- [Netdata — Xid 31: Memory Page Fault](https://www.netdata.cloud/guides/nvidia-gpu/nvidia-gpu-xid-31-memory-page-fault/)