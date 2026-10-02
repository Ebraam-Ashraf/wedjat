#include <vmlinux.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "../common.h"

static __always_inline struct ctx_key process_ctx_key(u64 pid_tgid, u64 ctx)
{
    struct ctx_key key = { .tgid = (u32)(pid_tgid >> 32), .ctx = ctx };
    return key;
}

static __always_inline void bind_context(u64 ctx)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct thread_key thread = { .pid_tgid = pid_tgid };
    struct ctx_key key = process_ctx_key(pid_tgid, ctx);
    struct device_binding *device = bpf_map_lookup_elem(&ctx_to_device, &key);
    struct task_struct *task = (struct task_struct *)bpf_get_current_task_btf();
    u64 start_boottime_ns = BPF_CORE_READ(task, start_boottime);

    if (!ctx) {
        /* Explicit NULL context — clear the thread binding. */
        bpf_map_delete_elem(&tid_to_device, &thread);
        return;
    }
    if (!device || device->start_boottime_ns != start_boottime_ns) {
        /* Unknown contexts must not inherit the thread's previous GPU. */
        bpf_map_delete_elem(&tid_to_device, &thread);
        if (device)
            bpf_map_delete_elem(&ctx_to_device, &key);
        return;
    }
    bpf_map_update_elem(&tid_to_device, &thread, device, BPF_ANY);
}

static __always_inline int remember_context_output(void *out, u32 device, u32 api_id)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct inflight_key key = make_inflight_key(pid_tgid, api_id);
    struct inflight_val value = { .arg1 = (u64)out, .device_ordinal = device };

    if (bpf_map_update_elem(&ctx_inflight_map, &key, &value, BPF_ANY) != 0)
        stats_add(api_id, STAT_MAP_UPDATE_FAILURE);
    return 0;
}

static __always_inline int finish_context_create(long ret, u32 api_id, bool bind)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct inflight_key key = make_inflight_key(pid_tgid, api_id);
    struct inflight_val *value = bpf_map_lookup_elem(&ctx_inflight_map, &key);
    u64 ctx = 0;

    if (!value)
        return 0;
    if (ret == 0 && bpf_probe_read_user(&ctx, sizeof(ctx), (void *)value->arg1) == 0 && ctx) {
        struct ctx_key context = process_ctx_key(pid_tgid, ctx);
        struct task_struct *task = (struct task_struct *)bpf_get_current_task_btf();
        struct device_binding binding = {
            .device_ordinal = value->device_ordinal,
            .start_boottime_ns = BPF_CORE_READ(task, start_boottime),
        };
        if (bpf_map_update_elem(&ctx_to_device, &context, &binding, BPF_ANY) != 0)
            stats_add(EVENT_CTX_CREATE, STAT_MAP_UPDATE_FAILURE);
        if (bind)
            bind_context(ctx);

        struct event event = {};
        init_event(&event, EVENT_CTX_CREATE);
        event.device_ordinal = value->device_ordinal;
        if (value->device_ordinal == WEDJAT_UNKNOWN_DEVICE)
            event.flags |= EVENT_F_DEVICE_UNKNOWN;
        else
            event.flags &= ~EVENT_F_DEVICE_UNKNOWN;
        event.address = ctx;
        event.status = (s32)ret;
        submit_event(&event);
    }
    bpf_map_delete_elem(&ctx_inflight_map, &key);
    return 0;
}

SEC("uprobe/cuCtxCreate_v2")
int BPF_KPROBE(trace_cuCtxCreate_v2, void *out, u32 flags, u32 device)
{
    return remember_context_output(out, device, EVENT_CTX_CREATE);
}
SEC("uretprobe/cuCtxCreate_v2")
int BPF_KRETPROBE(trace_cuCtxCreate_v2_ret, long ret)
{
    return finish_context_create(ret, EVENT_CTX_CREATE, true);
}

SEC("uprobe/cuCtxCreate_v3")
int BPF_KPROBE(trace_cuCtxCreate_v3, void *out, void *params, int count, u32 flags, u32 device)
{
    return remember_context_output(out, device, EVENT_CTX_CREATE);
}
SEC("uretprobe/cuCtxCreate_v3")
int BPF_KRETPROBE(trace_cuCtxCreate_v3_ret, long ret)
{
    return finish_context_create(ret, EVENT_CTX_CREATE, true);
}

SEC("uprobe/cuCtxCreate_v4")
int BPF_KPROBE(trace_cuCtxCreate_v4, void *out, void *params, u32 flags, u32 device)
{
    return remember_context_output(out, device, EVENT_CTX_CREATE);
}
SEC("uretprobe/cuCtxCreate_v4")
int BPF_KRETPROBE(trace_cuCtxCreate_v4_ret, long ret)
{
    return finish_context_create(ret, EVENT_CTX_CREATE, true);
}

SEC("uprobe/cuDevicePrimaryCtxRetain")
int BPF_KPROBE(trace_cuDevicePrimaryCtxRetain, void *out, u32 device)
{
    return remember_context_output(out, device, EVENT_CTX_CREATE);
}
SEC("uretprobe/cuDevicePrimaryCtxRetain")
int BPF_KRETPROBE(trace_cuDevicePrimaryCtxRetain_ret, long ret)
{
    return finish_context_create(ret, EVENT_CTX_CREATE, false);
}

SEC("uprobe/cuCtxSetCurrent")
int BPF_KPROBE(trace_cuCtxSetCurrent, void *cu_ctx)
{
    bind_context((u64)cu_ctx);
    return 0;
}

SEC("uprobe/cuCtxSetCurrent_ptsz")
int BPF_KPROBE(trace_cuCtxSetCurrent_ptsz, void *cu_ctx)
{
    bind_context((u64)cu_ctx);
    return 0;
}

SEC("uprobe/cuCtxPushCurrent_v2")
int BPF_KPROBE(trace_cuCtxPushCurrent_v2, void *cu_ctx)
{
    bind_context((u64)cu_ctx);
    return 0;
}

SEC("uprobe/cuCtxPopCurrent_v2")
int BPF_KPROBE(trace_cuCtxPopCurrent_v2, void *out)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct inflight_key key = make_inflight_key(pid_tgid, EVENT_CTX_POP);
    struct inflight_val value = { .arg1 = (u64)out };

    if (bpf_map_update_elem(&ctx_inflight_map, &key, &value, BPF_ANY) != 0)
        stats_add(EVENT_CTX_POP, STAT_MAP_UPDATE_FAILURE);
    return 0;
}

SEC("uretprobe/cuCtxPopCurrent_v2")
int BPF_KRETPROBE(trace_cuCtxPopCurrent_v2_ret, long ret)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct inflight_key key = make_inflight_key(pid_tgid, EVENT_CTX_POP);
    struct inflight_val *value = bpf_map_lookup_elem(&ctx_inflight_map, &key);

    /* cuCtxPopCurrent removes the top of the per-thread context stack and
     * restores the one below it.  We do not maintain a push-stack in BPF, so
     * we cannot know which device the restored context belongs to.  Clear the
     * thread's device binding on success; the next cuCtxSetCurrent / API call
     * that goes through bind_context will re-establish it.  Calls made in the
     * window between the pop and that re-establishment will carry
     * EVENT_F_DEVICE_UNKNOWN. */
    if (value && ret == 0) {
        struct thread_key thread = { .pid_tgid = pid_tgid };
        bpf_map_delete_elem(&tid_to_device, &thread);
    }
    bpf_map_delete_elem(&ctx_inflight_map, &key);
    return 0;
}

SEC("uprobe/cuCtxDestroy_v2")
int BPF_KPROBE(trace_cuCtxDestroy_v2, void *cu_ctx)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct ctx_key key = process_ctx_key(pid_tgid, (u64)cu_ctx);
    struct device_binding *device = bpf_map_lookup_elem(&ctx_to_device, &key);
    struct task_struct *task = (struct task_struct *)bpf_get_current_task_btf();
    u64 start_boottime_ns = BPF_CORE_READ(task, start_boottime);

    if (device && device->start_boottime_ns == start_boottime_ns) {
        struct event event = {};
        init_event(&event, EVENT_CTX_DESTROY);
        event.device_ordinal = device->device_ordinal;
        event.address = (u64)cu_ctx;
        submit_event(&event);
    }
    bpf_map_delete_elem(&ctx_to_device, &key);
    return 0;
}

/* CUDA exposes only a device ordinal here. Context entries are bounded by LRU. */
SEC("uprobe/cuDevicePrimaryCtxRelease_v2")
int BPF_KPROBE(trace_cuDevicePrimaryCtxRelease_v2, u32 device)
{
    return 0;
}

char LICENSE[] SEC("license") = "GPL";
