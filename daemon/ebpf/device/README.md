# Layer 5: Device-Resident GPU Micro-Telemetry

This directory is a placeholder for future GPU-side instrumentation.

There is no working BPF program here, and that is intentional.

---

## The Core Constraint

Linux eBPF runs on the **host CPU**, inside the Linux kernel.

A CUDA `__global__` function runs on the **GPU**, on Streaming Multiprocessors
(SMs), in a completely separate execution environment. The GPU has its own
instruction set (PTX/SASS), its own register file, and no connection to the
Linux kernel's BPF infrastructure.

This means:

```text
SEC("kprobe/my_cuda_kernel")   <- does NOT hook a CUDA kernel function.
                                  "my_cuda_kernel" is not a Linux kernel
                                  symbol. It is not in /proc/kallsyms.
                                  The loader will fail to attach.
```

This is not a tooling gap — it is an architectural boundary between two
separate execution domains.

---

## What Layer 5 Wants to Answer

Questions that can only be answered from inside the GPU:

```text
Which SM executed each thread block?      %smid
Which warp/lane was active?               %warpid / %laneid
Was there warp divergence?
Were global memory accesses coalesced?
Where are instruction-level stalls?       LG stalls, memory pipe, etc.
What is per-block SM utilization?
```

Layers 1–4 (uprobes, kprobes) answer host-side questions: who called CUDA,
what parameters, how long did the driver take, did UVM fault. Layer 5 is
the GPU-side complement — and it requires a completely different approach.

---

## How Layer 5 Can Be Implemented

### Option 1 — CUPTI (most realistic first step)

NVIDIA's official Compute Unified Device Infrastructure profiling API.
Runs on the host but the driver injects reads into the GPU's hardware
performance monitor units. Gives SM utilization, warp stall reasons,
memory throughput, etc.

Works today without modifying PTX or GPU binaries.

Limitation: requires elevated privileges. Does not give per-warp or
per-instruction granularity.

### Option 2 — PTX instrumentation

Intercept the PTX before the driver compiles it to SASS. Insert
instrumentation instructions (`%smid` reads, writes to a device buffer),
then let the driver compile the modified PTX. The inserted code runs on
the SM and writes to a device-side buffer; the host reads it back.

Realistic path: intercept at the `libcuda.so` / `nvrtc` boundary using
the uprobes already in `host_ctx.bpf.c` and `cuda_actions.bpf.c`, combined
with a host-side PTX transformer.

### Option 3 — NV Perf SDK / Nsight

Higher-level SDK built on top of CUPTI. Less flexible but faster to
produce results. Good reference for what CUPTI can measure.

### Option 4 — bpftime research

bpftime runs eBPF programs in userspace via dynamic instrumentation.
Not production-ready for GPU kernels. Worth watching, not worth building
on yet.

---

## When to Come Back Here

1. Layers 1–4 are working and their tests pass.
2. You have decided whether CUPTI gives sufficient signal.
3. If CUPTI is not enough, you have a plan for PTX interception.

Until then, `device_sm.bpf.c` is intentionally empty of executable code.
