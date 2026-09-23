/*
 * daemon/ebpf/device/device_sm.bpf.c
 *
 * Layer 5: Device-Resident GPU Micro-Telemetry.
 *
 * ============================================================
 * THIS FILE DOES NOT CONTAIN A WORKING BPF PROGRAM.
 * READ THE SECTION BELOW BEFORE WRITING ANY CODE HERE.
 * ============================================================
 *
 *
 * WHY THERE IS NO BPF CODE HERE
 * ------------------------------
 *
 * Linux eBPF programs run on the HOST CPU, inside the Linux kernel.
 * They attach to Linux kernel symbols (kprobes), Linux userspace symbols
 * (uprobes), or Linux kernel tracepoints.
 *
 * A CUDA __global__ function runs on the GPU, on Streaming Multiprocessors
 * (SMs), in a completely separate execution environment that has:
 *   - its own instruction set (PTX / SASS)
 *   - its own register file
 *   - no access to the Linux kernel
 *   - no BPF verifier
 *   - no BPF helpers
 *   - no connection to the host kernel's ring buffers or maps
 *
 * Writing:
 *
 *   SEC("kprobe/my_cuda_kernel")
 *   int BPF_KPROBE(handle_my_cuda_kernel) { ... }
 *
 * does NOT hook a CUDA kernel function.  "my_cuda_kernel" is not a Linux
 * kernel symbol.  It does not appear in /proc/kallsyms.  The BPF loader
 * would simply fail to find the symbol and refuse to attach.
 *
 * This is not a tooling limitation that can be worked around — it is a
 * fundamental architectural boundary.  The GPU and the Linux kernel are
 * separate execution domains.
 *
 *
 * WHAT LAYER 5 ACTUALLY WANTS TO ANSWER
 * ---------------------------------------
 *
 *   - Which SM executed each thread block?           %smid
 *   - Which warp/lane was active at each cycle?      %warpid / %laneid
 *   - Were there warp divergence stalls?
 *   - Were global memory accesses coalesced?
 *   - Where are instruction-level stalls?             e.g. LG stalls, memory pipe
 *   - What is the actual SM utilization per block?
 *
 * None of these questions can be answered from the host CPU at all — you need
 * to be running on the SM itself to observe SM-level state.
 *
 *
 * HOW LAYER 5 CAN EVENTUALLY BE IMPLEMENTED
 * -------------------------------------------
 *
 * Option 1 — CUPTI (most realistic first step):
 *
 *   NVIDIA's official Compute Unified Device Infrastructure profiling API.
 *   Runs on the host, but the NVIDIA driver injects counter reads into the
 *   GPU's hardware performance monitor units.  Gives SM utilization, warp
 *   stall reasons, memory throughput, etc.  Works today without modifying
 *   PTX or GPU binaries.
 *
 *   Limitation: requires root or CAP_PERFMON equivalent on newer drivers.
 *   Does not give per-warp / per-instruction granularity.
 *
 * Option 2 — PTX instrumentation:
 *
 *   Intercept the PTX before the driver compiles it to SASS, insert
 *   instrumentation instructions (%smid reads, __assert, etc.), then let
 *   the driver compile the modified PTX.  The inserted code runs on the SM
 *   and writes to a device-side buffer, which the host reads back.
 *
 *   Realistic path: intercept at the libcuda.so / nvrtc boundary using
 *   the uprobes in host_ctx.bpf.c and cuda_actions.bpf.c, combined with
 *   a host-side PTX transformer.
 *
 * Option 3 — NV Perf SDK / Nsight toolchain:
 *
 *   NVIDIA's higher-level SDK built on top of CUPTI.  Abstracts the
 *   hardware counter details.  Less flexible but faster to get results.
 *
 * Option 4 — bpftime / eGPU research:
 *
 *   Speculative.  bpftime runs eBPF in userspace via dynamic instrumentation.
 *   Not production-ready for GPU kernels.  Worth watching, not worth building
 *   on yet.
 *
 *
 * WHEN TO COME BACK TO THIS FILE
 * --------------------------------
 *
 *   1. Layers 1–4 (host_ctx, cuda_actions, driver_kprobes) are working
 *      and their tests pass.
 *   2. You have decided whether CUPTI gives enough signal.
 *   3. If CUPTI is not enough, you have a plan for PTX interception.
 *
 * Until then, this file is intentionally empty of executable code.
 * See device/README.md for the full Layer 5 design reference.
 */
