// https://eunomia.dev/tutorials/5-uprobe-bashreadline/

#include <vmlinux.h>
#include <bpf/bpf_helpers.h> 
#include <bpf/bpf_tracing.h>
#include "../common.h"


// 1. Thread opens the GPU device file (/dev/nvidia0, /dev/nvidiactl, etc.).
//    The very first OS-level action before any CUDA call can happen.
SEC("kprobe/do_sys_openat2")
int BPF_KPROBE(trace_do_sys_openat2, int dfd, const char *filename)
{
    // filename is a userspace pointer here (do_sys_openat2 calls getname() on
    // it internally) — read enough of it to check for the "/dev/nvidia" prefix.
    // common.h's event enum has no id yet for "device opened", so this stays
    // a filter-only stage marker; nothing is pushed to events_pipe.
    char path[16] = {};

    if(bpf_probe_read_user_str(&path, sizeof(path), filename) < 0)
    {
        return 0;
    }

    if(path[0] != '/' || path[1] != 'd' || path[2] != 'e' || path[3] != 'v' ||
       path[4] != '/' || path[5] != 'n' || path[6] != 'v' || path[7] != 'i' ||
       path[8] != 'd' || path[9] != 'i' || path[10] != 'a')
    {
        return 0;
    }

    return 0;
}

// 2. The GPU driver maps device memory / MMIO registers into the process address space.
//    Lets the CPU read/write GPU memory directly without going through ioctl every time.
SEC("kprobe/nvidia_mmap")
int BPF_KPROBE(trace_nvidia_mmap, void *file, void *vma)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = (u64)vma;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

SEC("kretprobe/nvidia_mmap")
int BPF_KRETPROBE(trace_nvidia_mmap_ret, long ret)
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
        e->api_id     = EVENT_MMAP;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0;
        e->address    = val->arg1;
        e->bytes      = 0;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// 3. Raw ioctl sent to the nvidia kernel driver — the low-level channel the CUDA runtime
//    uses to send every command to the GPU hardware (init, alloc, launch, teardown, all of it).
SEC("kprobe/nvidia_ioctl")
int BPF_KPROBE(trace_nvidia_ioctl, void *filp, u32 cmd, u64 arg)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = (u64)cmd;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

SEC("kretprobe/nvidia_ioctl")
int BPF_KRETPROBE(trace_nvidia_ioctl_ret, long ret)
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
        e->api_id     = EVENT_IOCTL;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0;
        e->address    = 0;
        e->bytes      = val->arg1; // ioctl cmd number, reusing bytes as a generic payload slot
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// 4. Raw ioctl sent to the UVM (Unified Virtual Memory) driver specifically.
//    Separate from nvidia_ioctl — handles the shared CPU/GPU virtual address space setup.
SEC("kprobe/uvm_ioctl")
int BPF_KPROBE(trace_uvm_ioctl, void *filp, u32 cmd, u64 arg)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = (u64)cmd;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

SEC("kretprobe/uvm_ioctl")
int BPF_KRETPROBE(trace_uvm_ioctl_ret, long ret)
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
        e->api_id     = EVENT_IOCTL;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0;
        e->address    = 0;
        e->bytes      = val->arg1;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. GPU triggered a page fault on a UVM address — fires mid-execution whenever the GPU
//    tries to access a page still sitting on the CPU. Kernel pauses GPU, migrates the page, resumes.
//    No fixed order, fires many times.
SEC("kprobe/uvm_va_block_service_fault")
int BPF_KPROBE(trace_uvm_va_block_service_fault)
{
    // uvm_va_block_service_fault's real argument list isn't public/stable
    // across driver versions, so no args are read here — just the stopwatch.
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = 0;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

SEC("kretprobe/uvm_va_block_service_fault")
int BPF_KRETPROBE(trace_uvm_va_block_service_fault_ret, long ret)
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
        e->api_id     = EVENT_UVM_FAULT;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0;
        e->address    = 0;
        e->bytes      = 0;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. Explicit UVM range migration between CPU and GPU memory (e.g. from cuMemPrefetchAsync).
//    No fixed order, fires many times.
SEC("kprobe/uvm_migrate")
int BPF_KPROBE(trace_uvm_migrate)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = 0;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

SEC("kretprobe/uvm_migrate")
int BPF_KRETPROBE(trace_uvm_migrate_ret, long ret)
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
        e->api_id     = EVENT_UVM_MIGRATE;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0;
        e->address    = 0;
        e->bytes      = 0;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. GPU memory pressure — UVM pages evicted back to CPU RAM to make room.
//    No fixed order, fires under memory pressure.
SEC("kprobe/uvm_va_block_evict_pages")
int BPF_KPROBE(trace_uvm_va_block_evict_pages)
{
    u32 tid = (u32)bpf_get_current_pid_tgid();

    struct inflight_val val = {};
    val.start_ts_ns = bpf_ktime_get_ns();
    val.arg1        = 0;

    bpf_map_update_elem(&inflight_map, &tid, &val, BPF_ANY);

    return 0;
}

SEC("kretprobe/uvm_va_block_evict_pages")
int BPF_KRETPROBE(trace_uvm_va_block_evict_pages_ret, long ret)
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
        e->api_id     = EVENT_UVM_EVICT;
        e->ts_ns      = bpf_ktime_get_ns();
        e->latency_ns = e->ts_ns - val->start_ts_ns;
        e->pid        = (u32)(pid_tgid >> 32);
        e->tid        = tid;
        e->device_id  = dev ? *dev : 0;
        e->address    = 0;
        e->bytes      = 0;
        e->status     = (s32)ret;

        bpf_ringbuf_submit(e, 0);
    }

    bpf_map_delete_elem(&inflight_map, &tid);

    return 0;
}

// n. Kernel spin-lock contention inside the GPU driver — fires when multiple threads
//    compete for the same driver resource. No fixed order.
SEC("kprobe/queued_spin_lock_slowpath")
int BPF_KPROBE(trace_queued_spin_lock_slowpath, void *lock, u32 val)
{
    // No dedicated event id for lock contention in common.h yet, and this
    // hook can fire very hot under contention — left as a no-op stub until
    // there's an aggregation path (e.g. a per-lock counter map) for it.

    return 0;
}

char LICENSE[] SEC("license") = "GPL";