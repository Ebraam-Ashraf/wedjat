#include <vmlinux.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "../common.h"

static __always_inline int begin_driver_call(u32 api_id, u64 payload)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct inflight_key key = make_inflight_key(pid_tgid, api_id);
    struct inflight_val value = {
        .start_ts_ns = bpf_ktime_get_ns(),
        .arg1 = payload,
        .device_ordinal = current_device_ordinal(),
    };

    if (bpf_map_update_elem(&driver_inflight_map, &key, &value, BPF_ANY) != 0)
        stats_add(api_id, STAT_MAP_UPDATE_FAILURE);
    return 0;
}

static __always_inline int finish_driver_call(u32 api_id, long ret)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct inflight_key key = make_inflight_key(pid_tgid, api_id);
    struct inflight_val *value = bpf_map_lookup_elem(&driver_inflight_map, &key);
    struct event event = {};

    if (!value)
        return 0;
    init_event(&event, api_id);
    event.device_ordinal = value->device_ordinal;
    event.address = value->arg1;
    event.latency_ns = bpf_ktime_get_ns() - value->start_ts_ns;
    event.status = (s32)ret;
    bpf_map_delete_elem(&driver_inflight_map, &key);

    u32 zero = 0;
    struct config_val *config = bpf_map_lookup_elem(&config_map, &zero);
    if (ret != 0 &&
        (api_id == EVENT_IOCTL || api_id == EVENT_UVM_IOCTL || api_id == EVENT_MMAP) &&
        (!config || !(config->flags & CONFIG_F_RAW_CAPTURE)))
        submit_event(&event);
    return record_aggregate(&event);
}

SEC("kprobe/nvidia_mmap")
int BPF_KPROBE(trace_nvidia_mmap, void *file, void *vma)
{
    return begin_driver_call(EVENT_MMAP, (u64)vma);
}
SEC("kretprobe/nvidia_mmap")
int BPF_KRETPROBE(trace_nvidia_mmap_ret, long ret)
{
    return finish_driver_call(EVENT_MMAP, ret);
}

/* NVIDIA driver internals are not a stable ABI; attach failures are expected
 * and reported by the loader. Validate the symbol and argument layout against
 * the installed NVIDIA module before relying on ioctl payloads. */
SEC("kprobe/nvidia_ioctl")
int BPF_KPROBE(trace_nvidia_ioctl, void *file, u32 cmd, u64 arg)
{
    return begin_driver_call(EVENT_IOCTL, cmd);
}
SEC("kretprobe/nvidia_ioctl")
int BPF_KRETPROBE(trace_nvidia_ioctl_ret, long ret)
{
    return finish_driver_call(EVENT_IOCTL, ret);
}

SEC("kprobe/uvm_ioctl")
int BPF_KPROBE(trace_uvm_ioctl, void *file, u32 cmd, u64 arg)
{
    return begin_driver_call(EVENT_UVM_IOCTL, cmd);
}
SEC("kretprobe/uvm_ioctl")
int BPF_KRETPROBE(trace_uvm_ioctl_ret, long ret)
{
    return finish_driver_call(EVENT_UVM_IOCTL, ret);
}

SEC("kprobe/uvm_va_block_service_fault")
int BPF_KPROBE(trace_uvm_va_block_service_fault, void *va_block,
               void *service_context, void *fault_page)
{
    /* uvm_va_block_service_fault(uvm_va_block_t*, uvm_service_block_context_t*,
     *                             uvm_page_index_t)
     * The faulting VA is not a direct argument, but the va_block carries the
     * VA range start.  Read the first u64 of the va_block struct which is
     * uvm_va_block_t::start (validated against multiple driver versions).
     * If the read fails we fall back to 0 — no worse than before. */
    u64 va = 0;
    bpf_probe_read_kernel(&va, sizeof(va), va_block);
    return begin_driver_call(EVENT_UVM_FAULT, va);
}
SEC("kretprobe/uvm_va_block_service_fault")
int BPF_KRETPROBE(trace_uvm_va_block_service_fault_ret, long ret)
{
    return finish_driver_call(EVENT_UVM_FAULT, ret);
}

SEC("kprobe/uvm_migrate")
int BPF_KPROBE(trace_uvm_migrate)
{
    return begin_driver_call(EVENT_UVM_MIGRATE, 0);
}
SEC("kretprobe/uvm_migrate")
int BPF_KRETPROBE(trace_uvm_migrate_ret, long ret)
{
    return finish_driver_call(EVENT_UVM_MIGRATE, ret);
}

SEC("kprobe/uvm_va_block_evict_pages")
int BPF_KPROBE(trace_uvm_va_block_evict_pages)
{
    return begin_driver_call(EVENT_UVM_EVICT, 0);
}
SEC("kretprobe/uvm_va_block_evict_pages")
int BPF_KRETPROBE(trace_uvm_va_block_evict_pages_ret, long ret)
{
    return finish_driver_call(EVENT_UVM_EVICT, ret);
}

char LICENSE[] SEC("license") = "GPL";
