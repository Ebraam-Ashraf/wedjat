#include<vmlinux.h>
#include<bpf/bpf_helpers.h>
#include<bpf/bpf_tracing.h>
#include"../../common.h"


// https://docs.nvidia.com/cuda/cuda-driver-api/cuda_driver_api/group__CUDA__CTX.html#_CPPv415cuCtxSetCurrent9CUcontext

// 5. Context created — entry captures the target device ordinal (which GPU).
SEC("uprobe/cuCtxCreate_v2")
int BPF_KPROBE(trace_cuCtxCreate_v2, void *pctx, u32 flags, u32 dev)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    // stash the out-param address (arg1) and the device ordinal (start_ts_ns,
    // repurposed here as plain scratch — ctx create/destroy is bookkeeping
    // only, no latency is measured in this file) so the uretprobe below can
    // read back the freshly created ctx handle and pair it with its GPU.
    struct inflight_val val = {};
    val.arg1        = (u64)pctx;
    val.start_ts_ns = (u64)dev;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

// 6. Context created — return, the new ctx handle is now valid.
//    We write [ctx -> GPU ID] into ctx_to_device.
SEC("uretprobe/cuCtxCreate_v2")
int BPF_KRETPROBE(trace_cuCtxCreate_v2_ret, long ret)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val *val = bpf_map_lookup_elem(&inflight_map, &tid);
    if(!val)
    {
        return 0;
    }

    if(ret == 0)
    {
        u64 ctx = 0;
        u32 dev = (u32)val->start_ts_ns;

        // *pctx now holds the freshly created context handle.
        if(bpf_probe_read_user(&ctx, sizeof(ctx), (void *)val->arg1) == 0)
        {
            bpf_map_update_elem(&ctx_to_device, &ctx, &dev, BPF_ANY);
        }
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// 7. Primary context retained (implicit creation path) — entry captures the CUdevice ordinal.
SEC("uprobe/cuDevicePrimaryCtxRetain")
int BPF_KPROBE(trace_cuDevicePrimaryCtxRetain, void *pctx, u32 dev)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.arg1        = (u64)pctx;
    val.start_ts_ns = (u64)dev;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

// 8. Primary context retained — return, ctx handle is now valid.
//    We write [ctx -> GPU ID] into ctx_to_device.
SEC("uretprobe/cuDevicePrimaryCtxRetain")
int BPF_KRETPROBE(trace_cuDevicePrimaryCtxRetain_ret, long ret)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val *val = bpf_map_lookup_elem(&inflight_map, &tid);
    if(!val)
    {
        return 0;
    }

    if(ret == 0)
    {
        u64 ctx = 0;
        u32 dev = (u32)val->start_ts_ns;

        if(bpf_probe_read_user(&ctx, sizeof(ctx), (void *)val->arg1) == 0)
        {
            bpf_map_update_elem(&ctx_to_device, &ctx, &dev, BPF_ANY);
        }
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// 9. Thread binds to a context (SetCurrent path) — we look up ctx in ctx_to_device
//    and write [TID -> GPU ID] into tid_to_device.
/*
READ Map 1 (ctx_to_device): We look up ctx_B. The map tells us this context belongs to GPU 1.

WRITE Map 2 (tid_to_device): We update Thread 1001's current state. We overwrite the old entry. The map now reads [TID 1001 -> GPU 1].
1. The Switch Event (cuCtxSetCurrent)To change GPUs, the CPU thread calls cuCtxSetCurrent(new_ctx).Hooks Called:uprobe/cuCtxSetCurrent (or cuCtxSetCurrent_ptsz / cuCtxPushCurrent_v2)Map Operations:Lookup in ctx_to_device: Read new_ctx $\rightarrow$ returns GPU 1.Overwrite in tid_to_device: Update key TID with value GPU 1 using BPF_ANY.State Change:
*/
SEC("uprobe/cuCtxSetCurrent")
int BPF_KPROBE(trace_cuCtxSetCurrent,void *ctx)
{
    u32 tid = bpf_get_current_pid_tgid();

    if(!ctx)
    {

        return 0;
    }

    u64 ctx_key = (u64)ctx;
    u32 *dev = bpf_map_lookup_elem(&ctx_to_device, &ctx_key);
    if(!dev)
    {
        return 0;
    }

    bpf_map_update_elem(&tid_to_device, &tid, dev, BPF_ANY);

    return 0;
}

// 9. Same as cuCtxSetCurrent but for the POSIX thread-safe (_ptsz) stream variant.
SEC("uprobe/cuCtxSetCurrent_ptsz")
int BPF_KPROBE(trace_cuCtxSetCurrent_ptsz, void *ctx)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    if(!ctx)
    {
        return 0;
    }

    u64 ctx_key = (u64)ctx;
    u32 *dev = bpf_map_lookup_elem(&ctx_to_device, &ctx_key);
    if(!dev)
    {
        return 0;
    }

    bpf_map_update_elem(&tid_to_device, &tid, dev, BPF_ANY);

    return 0;
}

// 10. Thread pushes a context onto its private stack (alternative bind path).
//     We look up ctx in ctx_to_device and write [TID -> GPU ID] into tid_to_device.
SEC("uprobe/cuCtxPushCurrent_v2")
int BPF_KPROBE(trace_cuCtxPushCurrent_v2, void *ctx)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    if(!ctx)
    {
        return 0;
    }

    u64 ctx_key = (u64)ctx;
    u32 *dev = bpf_map_lookup_elem(&ctx_to_device, &ctx_key);
    if(!dev)
    {
        return 0;
    }

    bpf_map_update_elem(&tid_to_device, &tid, dev, BPF_ANY);

    return 0;
}

// --- n. all the work happens here (see cuda_actions.bpf.c) ---

// 11. Thread pops the top context off its stack — we remove the TID from tid_to_device.
SEC("uprobe/cuCtxPopCurrent_v2")
int BPF_KPROBE(trace_cuCtxPopCurrent_v2, void *pctx)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    bpf_map_delete_elem(&tid_to_device, &tid);

    return 0;
}

// 12. Explicit context destruction — GPU session ends.
//     We delete the ctx key from ctx_to_device.
SEC("uprobe/cuCtxDestroy_v2")
int BPF_KPROBE(trace_cuCtxDestroy_v2, void *ctx)
{
    u64 ctx_key = (u64)ctx;

    bpf_map_delete_elem(&ctx_to_device, &ctx_key);

    return 0;
}

// 13. Primary context released — mirror of step 7/8.
//     We delete the ctx key from ctx_to_device.
SEC("uprobe/cuDevicePrimaryCtxRelease_v2")
int BPF_KPROBE(trace_cuDevicePrimaryCtxRelease_v2, u32 dev)
{
    // cuDevicePrimaryCtxRelease_v2 only gives us the device ordinal, not the
    // ctx handle — and ctx_to_device is keyed by ctx, not by device — so we
    // can't resolve which key to delete from here alone. No-op for now.

    return 0;
}

char LICENSE[] SEC("license") = "GPL";