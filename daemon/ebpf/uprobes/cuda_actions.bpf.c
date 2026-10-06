#include <vmlinux.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "../common.h"

static __always_inline int record_cuda(u32 api_id, u64 address, u64 bytes,
                                       u64 latency_ns, s32 status) {
    struct event event = {};
    init_event(&event, api_id);
    event.address = address;
    event.bytes = bytes;
    event.latency_ns = latency_ns;
    event.status = status;
    return record_aggregate(&event);
}

static __always_inline int begin_cuda_call(u32 api_id, u64 arg1, u64 arg2) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct inflight_key key = make_inflight_key(pid_tgid, api_id);
    struct inflight_val value = {
        .start_ts_ns = bpf_ktime_get_ns(),
        .arg1 = arg1,
        .arg2 = arg2,
        .device_ordinal = current_device_ordinal(),
    };

    if (bpf_map_update_elem(&cuda_inflight_map, &key, &value, BPF_ANY) != 0)
        stats_add(api_id, STAT_MAP_UPDATE_FAILURE);
    return 0;
}

static __always_inline int finish_cuda_call(u32 api_id, long ret) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct task_struct *task = (struct task_struct *)bpf_get_current_task_btf();
    u64 process_start = BPF_CORE_READ(task, start_boottime);
    struct inflight_key key = make_inflight_key(pid_tgid, api_id);
    struct inflight_val *value = bpf_map_lookup_elem(&cuda_inflight_map, &key);
    u64 now = bpf_ktime_get_ns();
    u64 address = 0;
    u64 bytes, latency;
    u32 device;
    u64 call_arg1;

    if (!value)
        return 0;

    latency = now - value->start_ts_ns;
    bytes = (api_id == EVENT_ALLOC) ? value->arg1 : 0;
    device = value->device_ordinal;
    call_arg1 = value->arg1;

    if (api_id == EVENT_ALLOC && ret == 0) {
        struct alloc_key alloc_key = {
            .tgid = (u32)(pid_tgid >> 32),
            .address = 0,
        };
        if (bpf_probe_read_user(&address, sizeof(address), (void *)value->arg2) == 0 &&
            address) {
            struct alloc_val alloc_value = {
                .bytes = bytes,
                .device_ordinal = device,
                .start_boottime_ns = process_start,
            };
            alloc_key.address = address;
            if (bpf_map_update_elem(&alloc_map, &alloc_key, &alloc_value, BPF_ANY) != 0)
                stats_add(EVENT_ALLOC, STAT_MAP_UPDATE_FAILURE);
        }
    } else if (api_id == EVENT_FREE && ret == 0) {
        struct alloc_key alloc_key = {
            .tgid = (u32)(pid_tgid >> 32),
            .address = call_arg1,
        };
        struct alloc_val *alloc_value = bpf_map_lookup_elem(&alloc_map, &alloc_key);
        if (alloc_value && alloc_value->start_boottime_ns == process_start) {
            bytes = alloc_value->bytes;
            device = alloc_value->device_ordinal;
            bpf_map_delete_elem(&alloc_map, &alloc_key);
        } else {
            if (alloc_value)
                bpf_map_delete_elem(&alloc_map, &alloc_key);
            stats_add(EVENT_FREE, STAT_ALLOC_FREE_MISS);
        }
        address = call_arg1;
    }
    bpf_map_delete_elem(&cuda_inflight_map, &key);

    /* Use the device ordinal captured at call entry, not a fresh lookup.
     * By this point the thread may have switched context. */
    struct event event = {};
    init_event(&event, api_id);
    event.device_ordinal = device;
    /* Preserve the unknown-device flag from init_event if the saved ordinal
     * happens to be WEDJAT_UNKNOWN_DEVICE. */
    if (device == WEDJAT_UNKNOWN_DEVICE)
        event.flags |= EVENT_F_DEVICE_UNKNOWN;
    else
        event.flags &= ~EVENT_F_DEVICE_UNKNOWN;
    event.address = address;
    event.bytes = bytes;
    event.latency_ns = latency;
    event.status = (s32)ret;
    if (api_id == EVENT_SYNC) {
        u32 zero = 0;
        struct config_val *config = bpf_map_lookup_elem(&config_map, &zero);
        if (config && config->sync_stall_us &&
            latency >= (u64)config->sync_stall_us * 1000 &&
            !(config->flags & CONFIG_F_RAW_CAPTURE))
            submit_event(&event);
    }
    return record_aggregate(&event);
}

static __always_inline int record_cuda_now(u32 api_id, u64 address, u64 bytes) {
    return record_cuda(api_id, address, bytes, 0, 0);
}

#define ALLOC_PROBES(name, symbol, size_type)                                          \
    SEC("uprobe/" symbol)                                                              \
    int BPF_KPROBE(trace_##name, void *out, size_type bytes) {                         \
        return begin_cuda_call(EVENT_ALLOC, bytes, (u64)out);                          \
    }                                                                                  \
    SEC("uretprobe/" symbol)                                                           \
    int BPF_KRETPROBE(trace_##name##_ret, long ret) {                                  \
        return finish_cuda_call(EVENT_ALLOC, ret);                                     \
    }

ALLOC_PROBES(cuMemAlloc_v2, "cuMemAlloc_v2", u64)
ALLOC_PROBES(cuMemAlloc, "cuMemAlloc", u64)
ALLOC_PROBES(cuMemAllocManaged, "cuMemAllocManaged", u64)
ALLOC_PROBES(cuMemAllocAsync, "cuMemAllocAsync", u64)
#undef ALLOC_PROBES

SEC("uprobe/cuMemFree_v2")
int BPF_KPROBE(trace_cuMemFree_v2, u64 ptr) {
    return begin_cuda_call(EVENT_FREE, ptr, 0);
}
SEC("uretprobe/cuMemFree_v2")
int BPF_KRETPROBE(trace_cuMemFree_v2_ret, long ret) {
    return finish_cuda_call(EVENT_FREE, ret);
}
SEC("uprobe/cuMemFree")
int BPF_KPROBE(trace_cuMemFree, u64 ptr) {
    return begin_cuda_call(EVENT_FREE, ptr, 0);
}
SEC("uretprobe/cuMemFree")
int BPF_KRETPROBE(trace_cuMemFree_ret, long ret) {
    return finish_cuda_call(EVENT_FREE, ret);
}
SEC("uprobe/cuMemFreeAsync")
int BPF_KPROBE(trace_cuMemFreeAsync, u64 ptr, void *stream) {
    return begin_cuda_call(EVENT_FREE, ptr, 0);
}
SEC("uretprobe/cuMemFreeAsync")
int BPF_KRETPROBE(trace_cuMemFreeAsync_ret, long ret) {
    return finish_cuda_call(EVENT_FREE, ret);
}

SEC("uprobe/cuMemcpyHtoD_v2")
int BPF_KPROBE(trace_cuMemcpyHtoD_v2, u64 dst, void *src, u64 bytes) {
    return record_cuda_now(EVENT_MEMCPY, dst, bytes);
}
SEC("uprobe/cuMemcpyDtoH_v2")
int BPF_KPROBE(trace_cuMemcpyDtoH_v2, void *dst, u64 src, u64 bytes) {
    return record_cuda_now(EVENT_MEMCPY, src, bytes);
}
SEC("uprobe/cuMemcpyDtoD_v2")
int BPF_KPROBE(trace_cuMemcpyDtoD_v2, u64 dst, u64 src, u64 bytes) {
    return record_cuda_now(EVENT_MEMCPY, dst, bytes);
}
SEC("uprobe/cuMemcpyHtoDAsync_v2")
int BPF_KPROBE(trace_cuMemcpyHtoDAsync_v2, u64 dst, void *src, u64 bytes,
               void *stream) {
    return record_cuda_now(EVENT_MEMCPY, dst, bytes);
}
SEC("uprobe/cuMemcpyDtoHAsync_v2")
int BPF_KPROBE(trace_cuMemcpyDtoHAsync_v2, void *dst, u64 src, u64 bytes,
               void *stream) {
    return record_cuda_now(EVENT_MEMCPY, src, bytes);
}
SEC("uprobe/cuMemcpyDtoDAsync_v2")
int BPF_KPROBE(trace_cuMemcpyDtoDAsync_v2, u64 dst, u64 src, u64 bytes, void *stream) {
    return record_cuda_now(EVENT_MEMCPY, dst, bytes);
}
SEC("uprobe/cuMemcpyAsync")
int BPF_KPROBE(trace_cuMemcpyAsync, u64 dst, u64 src, u64 bytes, u32 kind,
               void *stream) {
    return record_cuda_now(EVENT_MEMCPY, dst, bytes);
}

#define LAUNCH_PROBE(name, symbol, fn_arg)                                             \
    SEC("uprobe/" symbol)                                                              \
    int BPF_KPROBE(trace_##name, void *fn_arg) {                                       \
        return record_cuda_now(EVENT_LAUNCH, (u64)fn_arg, 0);                          \
    }

LAUNCH_PROBE(cuLaunchKernel, "cuLaunchKernel", fn)
LAUNCH_PROBE(cuLaunchKernel_ptsz, "cuLaunchKernel_ptsz", fn)
LAUNCH_PROBE(cuLaunchCooperativeKernel, "cuLaunchCooperativeKernel", fn)
LAUNCH_PROBE(cuGraphLaunch, "cuGraphLaunch", graph)
#undef LAUNCH_PROBE

#define SYNC_PROBES(name, symbol)                                                      \
    SEC("uprobe/" symbol)                                                              \
    int BPF_KPROBE(trace_##name, void *stream) {                                       \
        return begin_cuda_call(EVENT_SYNC, (u64)stream, 0);                            \
    }                                                                                  \
    SEC("uretprobe/" symbol)                                                           \
    int BPF_KRETPROBE(trace_##name##_ret, long ret) {                                  \
        return finish_cuda_call(EVENT_SYNC, ret);                                      \
    }

SYNC_PROBES(cuStreamSynchronize, "cuStreamSynchronize")
SYNC_PROBES(cuStreamSynchronize_ptsz, "cuStreamSynchronize_ptsz")
SYNC_PROBES(cuEventSynchronize, "cuEventSynchronize")
#undef SYNC_PROBES

SEC("uprobe/cuCtxSynchronize")
int BPF_KPROBE(trace_cuCtxSynchronize) {
    return begin_cuda_call(EVENT_SYNC, 0, 0);
}
SEC("uretprobe/cuCtxSynchronize")
int BPF_KRETPROBE(trace_cuCtxSynchronize_ret, long ret) {
    return finish_cuda_call(EVENT_SYNC, ret);
}

char LICENSE[] SEC("license") = "GPL";
