/*
 * LAYER 2: CUDA Driver API Intent
 *
 * Goal of this file:
 *   Capture what the application asked CUDA to do at the libcuda.so boundary.
 *
 * Layer 2 answers:
 *   - Did the app launch a kernel?
 *   - What grid/block/shared-memory/stream shape did it request?
 *   - Did it allocate GPU memory? How many bytes? Did it fail?
 *   - Did it copy memory? How many bytes and in what direction?
 *   - Did a CPU thread block waiting for GPU work? For how long?
 *
 * Mental model:
 *   Layer 1 is the envelope: who/where/when.
 *   Layer 2 is the CUDA intent: what operation/parameters.
 */

/*
 * SEC("uprobe/cuLaunchKernel")
 * SEC("uprobe/cuLaunchKernel_ptsz")
 * SEC("uprobe/cuLaunchKernelEx")
 *
 * WHY:
 *   Core GPU work-dispatch signal.
 *
 * Captures:
 *   - function pointer / kernel handle
 *   - gridDim x/y/z
 *   - blockDim x/y/z
 *   - sharedMem bytes
 *   - stream handle
 *
 * Notes:
 *   Keep a Layer 1 identity helper shared with host_ctx.bpf.c so every launch
 *   can be attributed to pid/tid/cgroup/device.
 */

/*
 * SEC("uprobe/cuMemAlloc")
 * SEC("uprobe/cuMemAlloc_v2")
 * SEC("uprobe/cuMemAllocAsync")
 *
 * WHY entry probe:
 *   The allocation size is known before the call runs.
 *
 * Captures on entry:
 *   - requested bytes
 *   - output pointer address, if the API writes CUdeviceptr through a pointer
 *   - start timestamp for latency
 */

/*
 * SEC("uretprobe/cuMemAlloc")
 * SEC("uretprobe/cuMemAlloc_v2")
 * SEC("uretprobe/cuMemAllocAsync")
 *
 * WHY return probe:
 *   Return code, final latency, and sometimes the actual allocated pointer are
 *   only known after libcuda returns.
 *
 * Captures on return:
 *   - CUDA result code
 *   - latency_ns = now - entry_ts
 *   - allocated pointer, if safe to read from user memory
 */

/*
 * SEC("uprobe/cuMemcpyAsync")
 * SEC("uprobe/cuMemcpyAsync_ptsz")
 *
 * WHY:
 *   PCIe/NVLink transfer intent signal.
 *
 * Captures:
 *   - byte count
 *   - source pointer
 *   - destination pointer
 *   - stream handle
 *
 * Direction note:
 *   cuMemcpyAsync itself may not directly carry a simple direction enum in the
 *   same way runtime cudaMemcpyAsync does. For clear H2D/D2H/D2D direction,
 *   also consider specific driver symbols if exported on your system:
 *     cuMemcpyHtoDAsync / cuMemcpyDtoHAsync / cuMemcpyDtoDAsync and _v2/_ptsz.
 */

/*
 * SEC("uprobe/cuStreamSynchronize")
 * SEC("uprobe/cuStreamSynchronize_ptsz")
 *
 * WHY entry probe:
 *   CPU thread begins waiting for GPU stream progress.
 *
 * Captures:
 *   - stream handle
 *   - start timestamp
 */

/*
 * SEC("uretprobe/cuStreamSynchronize")
 * SEC("uretprobe/cuStreamSynchronize_ptsz")
 *
 * WHY return probe:
 *   CPU thread finished waiting.
 *
 * Captures:
 *   - CUDA result code
 *   - stall latency in ns/us
 *
 * This is one of the most useful "why is my app slow?" hooks.
 */

/*
 * Missing before real implementation:
 *   1. Define inflight maps for entry/return pairing, keyed by pid_tgid.
 *   2. Define API ids for launch/malloc/memcpy/sync.
 *   3. Decide fast path vs slow path:
 *        fast path = per-CPU aggregate counters
 *        slow path = ringbuf only for errors, huge allocations, long stalls
 *   4. Confirm exact function signatures from CUDA Driver API docs/headers.
 *   5. Attach variants only after basic hooks pass tests.
 */
