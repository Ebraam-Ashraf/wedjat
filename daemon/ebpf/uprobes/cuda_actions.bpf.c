#include<vmlinux.h>
#include<bpf/bpf_helpers.h>
#include<bpf/bpf_tracing.h>
#include"../common.h"


// All probes here are // n — no fixed order, any can fire many times in any sequence.
// This is the work phase: between step 10 (context bound) and step 11 (context popped).
// Every probe reads [TID -> GPU ID] from tid_to_device, measures duration,
// and pushes a struct event to userspace. No map writes happen in this file.

/*
eBPF Hook: uprobe/cuMemAlloc (The Entry Probe)

Map Actions:

READ Map 2 (tid_to_device): We ask the kernel for the current thread ID (1001). We look up TID 1001 in Map 2. Because of the switch, the map instantly answers: "GPU 1".

WRITE Map 3 (inflight_map): We need a stopwatch to measure how long this takes. We write an entry into Map 3: [TID 1001 -> {start_time: 10:00:00.000, device_id: 1, size: 50MB}].
*/
// n. GPU memory allocation (v2 API) — entry captures requested size.
SEC("uprobe/cuMemAlloc_v2")
int BPF_KPROBE(trace_cuMemAlloc_v2, void *dptr, u64 bytesize)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    // stash the requested size as our stopwatch payload; the size that
    // actually got allocated only makes sense once status is known below.
    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = bytesize;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

// n. GPU memory allocation (v2 API) — return, allocated device pointer is valid.
SEC("uretprobe/cuMemAlloc_v2")
int BPF_KRETPROBE(trace_cuMemAlloc_v2_ret, long ret)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    struct inflight_val *val = bpf_map_lookup_elem(&inflight_map, &tid);
    if(!val)
    {
        return 0;
    }

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_ALLOC;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0xffffffff;
        e->address    = 0;
        e->bytes      = val->arg1;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. GPU memory allocation (legacy API) — entry captures requested size.
SEC("uprobe/cuMemAlloc")
int BPF_KPROBE(trace_cuMemAlloc, void *dptr, u32 bytesize)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = (u64)bytesize;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

// n. GPU memory allocation (legacy API) — return, allocated device pointer is valid.
SEC("uretprobe/cuMemAlloc")
int BPF_KRETPROBE(trace_cuMemAlloc_ret, long ret)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    struct inflight_val *val = bpf_map_lookup_elem(&inflight_map, &tid);
    if(!val)
    {
        return 0;
    }

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_ALLOC;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0xffffffff;
        e->address    = 0;
        e->bytes      = val->arg1;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. Unified Memory allocation — GPU and CPU share one virtual address space.
//    Page faults for this memory are handled by uvm_va_block_service_fault in driver_kprobes.bpf.c.
SEC("uprobe/cuMemAllocManaged")
int BPF_KPROBE(trace_cuMemAllocManaged, void *dptr, u64 bytesize, u32 flags)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    // no matching uretprobe declared for this one, so we log it straight
    // from entry — no latency, no confirmed status.
    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_ALLOC;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = 0;
        e->bytes      = bytesize;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Synchronous host-to-device copy (CPU RAM -> GPU VRAM). Blocks until done.
SEC("uprobe/cuMemcpyHtoD_v2")
int BPF_KPROBE(trace_cuMemcpyHtoD_v2, u64 dstDevice, void *srcHost, u64 ByteCount)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_MEMCPY;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = dstDevice;
        e->bytes      = ByteCount;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Synchronous device-to-host copy (GPU VRAM -> CPU RAM). Blocks until done.
SEC("uprobe/cuMemcpyDtoH_v2")
int BPF_KPROBE(trace_cuMemcpyDtoH_v2, void *dstHost, u64 srcDevice, u64 ByteCount)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_MEMCPY;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = srcDevice;
        e->bytes      = ByteCount;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Synchronous device-to-device copy (GPU VRAM -> GPU VRAM). Blocks until done.
SEC("uprobe/cuMemcpyDtoD_v2")
int BPF_KPROBE(trace_cuMemcpyDtoD_v2, u64 dstDevice, u64 srcDevice, u64 ByteCount)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_MEMCPY;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = dstDevice;
        e->bytes      = ByteCount;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Async host-to-device copy — returns immediately, GPU copies in the background.
SEC("uprobe/cuMemcpyHtoDAsync_v2")
int BPF_KPROBE(trace_cuMemcpyHtoDAsync_v2, u64 dstDevice, void *srcHost, u64 ByteCount)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    // hStream is the 4th arg — left unread here, struct event has no field for it.
    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_MEMCPY;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = dstDevice;
        e->bytes      = ByteCount;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Async device-to-host copy — returns immediately, GPU copies in the background.
SEC("uprobe/cuMemcpyDtoHAsync_v2")
int BPF_KPROBE(trace_cuMemcpyDtoHAsync_v2, void *dstHost, u64 srcDevice, u64 ByteCount)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_MEMCPY;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = srcDevice;
        e->bytes      = ByteCount;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Async device-to-device copy — returns immediately, GPU copies in the background.
SEC("uprobe/cuMemcpyDtoDAsync_v2")
int BPF_KPROBE(trace_cuMemcpyDtoDAsync_v2, u64 dstDevice, u64 srcDevice, u64 ByteCount)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_MEMCPY;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = dstDevice;
        e->bytes      = ByteCount;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Async memory copy — direction (HtoD / DtoH / DtoD) inferred at runtime from pointer attributes.
SEC("uprobe/cuMemcpyAsync")
int BPF_KPROBE(trace_cuMemcpyAsync, u64 dst, u64 src, u64 ByteCount)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_MEMCPY;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = dst;
        e->bytes      = ByteCount;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Resolves a kernel name to a function handle from a loaded module.
//    Called before launch to get the function pointer.
SEC("uprobe/cuModuleGetFunction")
int BPF_KPROBE(trace_cuModuleGetFunction, void *hfunc, void *hmod, const char *name)
{
    // Just a lookup, not GPU work — common.h's event enum has no id for this,
    // so nothing is pushed to events_pipe here. Kept as a hook point in case
    // it's ever needed (e.g. to resolve kernel names for cuLaunchKernel events).

    return 0;
}

// n. Kernel launch — thread commands the GPU to execute a function.
SEC("uprobe/cuLaunchKernel")
int BPF_KPROBE(trace_cuLaunchKernel, void *f, u32 gridDimX, u32 gridDimY, u32 gridDimZ, u32 blockDimX, u32 blockDimY)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    // blockDimZ, sharedMemBytes, hStream, kernelParams and extra sit past the
    // 6th register-passed argument on x86_64 (stack-spilled), so they're left
    // out here to keep this a plain register read for v1.
    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_LAUNCH;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = (u64)f;
        e->bytes      = 0;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Same as cuLaunchKernel but for the POSIX thread-safe (_ptsz) stream variant.
SEC("uprobe/cuLaunchKernel_ptsz")
int BPF_KPROBE(trace_cuLaunchKernel_ptsz, void *f, u32 gridDimX, u32 gridDimY, u32 gridDimZ, u32 blockDimX, u32 blockDimY)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_LAUNCH;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = (u64)f;
        e->bytes      = 0;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. Cooperative kernel launch — all thread blocks can synchronize across the whole GPU.
SEC("uprobe/cuLaunchCooperativeKernel")
int BPF_KPROBE(trace_cuLaunchCooperativeKernel, void *f, u32 gridDimX, u32 gridDimY, u32 gridDimZ, u32 blockDimX, u32 blockDimY)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_LAUNCH;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = (u64)f;
        e->bytes      = 0;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. CUDA Graph launch — replays a pre-recorded sequence of GPU ops as one atomic unit.
SEC("uprobe/cuGraphLaunch")
int BPF_KPROBE(trace_cuGraphLaunch, void *hGraphExec, void *hStream)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_LAUNCH;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = (u64)hGraphExec;
        e->bytes      = 0;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

// n. CPU blocks until all work on a specific stream finishes — entry records start time.
SEC("uprobe/cuStreamSynchronize")
int BPF_KPROBE(trace_cuStreamSynchronize, void *hStream)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = (u64)hStream;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

// n. Stream sync finished — return, pair with entry to measure total GPU wait time.
SEC("uretprobe/cuStreamSynchronize")
int BPF_KRETPROBE(trace_cuStreamSynchronize_ret, long ret)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    struct inflight_val *val = bpf_map_lookup_elem(&inflight_map, &tid);
    if(!val)
    {
        return 0;
    }

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_SYNC;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0xffffffff;
        e->address    = val->arg1;
        e->bytes      = 0;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. Same as cuStreamSynchronize but for the POSIX thread-safe (_ptsz) stream variant — entry.
SEC("uprobe/cuStreamSynchronize_ptsz")
int BPF_KPROBE(trace_cuStreamSynchronize_ptsz, void *hStream)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = (u64)hStream;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

// n. Stream sync (_ptsz) finished — return, pair with entry for duration.
SEC("uretprobe/cuStreamSynchronize_ptsz")
int BPF_KRETPROBE(trace_cuStreamSynchronize_ptsz_ret, long ret)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    struct inflight_val *val = bpf_map_lookup_elem(&inflight_map, &tid);
    if(!val)
    {
        return 0;
    }

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_SYNC;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0xffffffff;
        e->address    = val->arg1;
        e->bytes      = 0;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. CPU blocks until ALL work on ALL streams for the current context finishes — entry.
SEC("uprobe/cuCtxSynchronize")
int BPF_KPROBE(trace_cuCtxSynchronize)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = 0;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

// n. Full-context sync finished — return, pair with entry to measure total wait time.
SEC("uretprobe/cuCtxSynchronize")
int BPF_KRETPROBE(trace_cuCtxSynchronize_ret, long ret)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    struct inflight_val *val = bpf_map_lookup_elem(&inflight_map, &tid);
    if(!val)
    {
        return 0;
    }

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_SYNC;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0xffffffff;
        e->address    = 0;
        e->bytes      = 0;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. GPU memory free — the device pointer being released is the argument.
SEC("uprobe/cuMemFree_v2")
int BPF_KPROBE(trace_cuMemFree_v2, u64 dptr)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tid = (u32)pid_tgid;

    u32 *dev = bpf_map_lookup_elem(&tid_to_device, &tid);
    if(!dev)
    {
        return 0;
    }

    struct event *e = bpf_ringbuf_reserve(&events_pipe, sizeof(*e), 0);
    if(e)
    {
        e->api_id     = EVENT_FREE;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = 0;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = *dev;
        e->address    = dptr;
        e->bytes      = 0;
        e->status     = 0;

        bpf_ringbuf_submit(e, 0);
    }

    return 0;
}

char LICENSE[] SEC("license") = "GPL";