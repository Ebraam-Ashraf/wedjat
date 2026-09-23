/*
 * daemon/ebpf/kprobes/driver_kprobes.bpf.c
 *
 * Layer 3: NVIDIA Driver + Linux OS Subsystems.
 *
 * WHAT THIS FILE IS:
 *   eBPF programs that hook into the Linux kernel and the NVIDIA kernel module
 *   to observe what happens after CUDA intent (Layer 2) crosses into kernel
 *   space.  These are kprobes/kretprobes (kernel function hooks) and
 *   tracepoints (stable kernel instrumentation points), NOT uprobes.
 *
 *   The difference from uprobes/:
 *     uprobes  = hook into libcuda.so in userspace (Layer 1+2)
 *     kprobes  = hook into the kernel itself, including the nvidia.ko module
 *
 * LAYER 3 ANSWERS:
 *   - Did the process enter the NVIDIA kernel driver?   nvidia_unlocked_ioctl
 *   - Was the ioctl slow or did it return an error?     kretprobe latency
 *   - Is NVIDIA UVM faulting pages?                     uvm_vm_fault_entry
 *   - Is managed memory migrating between CPU/GPU?      uvm_migrate
 *   - Is VRAM being evicted under memory pressure?      uvm_pmm_gpu_pma_evict*
 *   - Is the CUDA thread being preempted by Linux?      sched_switch tracepoint
 *
 * SYMBOL STABILITY WARNING:
 *   nvidia_unlocked_ioctl and the uvm_* symbols are NVIDIA kernel module
 *   internals.  They can change or disappear between driver versions.  The
 *   loader (test harness or daemon) MUST check /proc/kallsyms at runtime
 *   and skip any probe whose symbol is absent — never fail hard on a missing
 *   NVIDIA symbol.  The sched_switch tracepoint is stable Linux ABI and
 *   will always be present.
 *
 * ATTACHMENT:
 *   These probes attach to kernel symbols, not userspace libraries.
 *   The loader uses bpf_program__attach_kprobe() (not attach_uprobe_opts).
 *   SEC names like "kprobe/nvidia_unlocked_ioctl" are resolved against
 *   /proc/kallsyms at load time.
 */

#include "vmlinux.h"           /* kernel type definitions                         */
#include <bpf/bpf_helpers.h>   /* bpf_map_*, bpf_ringbuf_*, bpf_get_current_*   */
#include <bpf/bpf_tracing.h>   /* BPF_KPROBE, BPF_KRETPROBE, PT_REGS_PARM*      */
#include <bpf/bpf_core_read.h> /* BPF_CORE_READ for CO-RE field access           */
#include "../../wedjat_common.h"

char LICENSE[] SEC("license") = "Dual BSD/GPL";

/* ============================================================
 * MAPS
 * ============================================================ */

/*
 * events — ring buffer for notable events (slow path).
 *
 * Not every ioctl goes here — that would be a firehose.
 * Only events that cross a threshold or indicate a problem:
 *   - ioctl latency > WEDJAT_SLOW_IOCTL_NS
 *   - ioctl return value != 0 (error)
 *   - UVM fault / migration / eviction (these are inherently notable)
 *   - sched_switch on a GPU-active thread
 */
struct {
    __uint(type,        BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 512 * 1024); /* 512 KiB — UVM events can be bursty */
} events SEC(".maps");

/*
 * ioctl_counts — per-CPU ioctl counters, keyed by pid.
 *
 * Fast path: every ioctl increments this without a ringbuf event.
 * The daemon reads and aggregates across CPUs periodically.
 */
struct {
    __uint(type,        BPF_MAP_TYPE_PERCPU_HASH);
    __uint(max_entries, 1024);
    __type(key,         u32);  /* pid */
    __type(value,       u64);  /* ioctl call count */
} ioctl_counts SEC(".maps");

/*
 * inflight_ioctls — tracks ioctl entry timestamps for latency measurement.
 *
 * key = pid_tgid (u64, unique per thread).
 * value = entry timestamp in ns.
 *
 * The kretprobe reads this, computes latency, then deletes the entry.
 * If the kretprobe fires without a matching entry (process died mid-ioctl,
 * or the map was full at entry time), it skips the latency calculation.
 */
struct {
    __uint(type,        BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key,         u64);  /* pid_tgid */
    __type(value,       u64);  /* entry timestamp_ns */
} inflight_ioctls SEC(".maps");

/*
 * gpu_active_tids — set of tids that recently called into the NVIDIA driver.
 *
 * Used by the sched_switch probe to decide whether a context switch is
 * relevant (i.e. a GPU-active thread got preempted).
 * Value is always 1; existence in the map means "this tid is GPU-active."
 * Entries are added at ioctl entry and removed at ioctl return.
 */
struct {
    __uint(type,        BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key,         u32);  /* tid */
    __type(value,       u8);   /* always 1 */
} gpu_active_tids SEC(".maps");

/* ============================================================
 * THRESHOLDS
 *
 * These control which ioctls get a ringbuf event vs just a counter
 * increment.  Defined as constants here so they can be overridden
 * at load time via the skeleton's rodata section in the future.
 * ============================================================ */

/* Ioctls slower than this get a ringbuf event. 1ms in nanoseconds. */
#define WEDJAT_SLOW_IOCTL_NS  (1000ULL * 1000ULL)

/* ============================================================
 * EVENT STRUCTS
 *
 * Each struct is written by a BPF probe and read by userspace.
 * Must match wedjat_common.h once that header is filled in.
 * Inline here so this file compiles independently.
 * ============================================================ */

/* Emitted by kretprobe/nvidia_unlocked_ioctl for slow or failed ioctls. */
struct ioctl_event {
    u64  timestamp_ns; /* when the ioctl returned                     */
    u64  latency_ns;   /* time inside nvidia_unlocked_ioctl            */
    u32  pid;
    u32  tid;
    u32  cmd;          /* ioctl command number (third arg)             */
    int  retval;       /* return value from nvidia_unlocked_ioctl      */
    char comm[16];
};

/* Emitted by kprobe/uvm_vm_fault_entry for each UVM page fault. */
struct uvm_fault_event {
    u64  timestamp_ns;
    u32  pid;
    u32  tid;
    char comm[16];
};

/* Emitted by kprobe/uvm_migrate* for managed-memory migrations. */
struct uvm_migrate_event {
    u64  timestamp_ns;
    u32  pid;
    u32  tid;
    char comm[16];
};

/* Emitted by tracepoint/sched/sched_switch when a GPU-active thread is
 * switched out.  Tells us the thread stopped running on CPU. */
struct preempt_event {
    u64  timestamp_ns;
    u32  prev_pid;     /* the GPU-active thread that was switched out  */
    u32  next_pid;     /* the thread that replaced it                  */
    char prev_comm[16];
    char next_comm[16];
};

/* api_id values embedded in events — matches wedjat_common.h when defined. */
#define API_DRIVER_IOCTL  10
#define API_UVM_FAULT     11
#define API_UVM_MIGRATE   12
#define API_UVM_EVICT     13
#define API_PREEMPT       14

/* ============================================================
 * KPROBE: nvidia_unlocked_ioctl (entry)
 *
 * nvidia_unlocked_ioctl is the real exported symbol in the NVIDIA kernel
 * module — visible in /proc/kallsyms as long as the driver is loaded.
 *
 * C signature (standard Linux file_operations.unlocked_ioctl):
 *
 *   long nvidia_unlocked_ioctl(
 *       struct file *filp,   // arg 1
 *       unsigned int cmd,    // arg 2 — ioctl command number
 *       unsigned long arg    // arg 3 — ioctl argument (pointer to user data)
 *   );
 *
 * We capture:
 *   - pid/tid for correlation with Layer 1/2 events
 *   - cmd for identifying which ioctl is slow
 *   - entry timestamp (stored in inflight_ioctls for the return probe)
 *   - mark this tid as GPU-active (for sched_switch)
 * ============================================================ */
SEC("kprobe/nvidia_unlocked_ioctl")
int BPF_KPROBE(handle_nvidia_ioctl_entry,
               struct file *filp,   /* arg 1 — file handle (unused here) */
               unsigned int cmd,    /* arg 2 — ioctl command number       */
               unsigned long arg)   /* arg 3 — ioctl argument pointer     */
{
    u64 pid_tgid   = bpf_get_current_pid_tgid();
    u32 pid        = (u32)(pid_tgid >> 32);
    u32 tid        = (u32)(pid_tgid);
    u64 now        = bpf_ktime_get_ns();

    /* Fast path: increment per-CPU ioctl counter for this pid. */
    u64 *count = bpf_map_lookup_elem(&ioctl_counts, &pid);
    if (count) {
        __sync_fetch_and_add(count, 1);
    } else {
        u64 init = 1;
        bpf_map_update_elem(&ioctl_counts, &pid, &init, BPF_ANY);
    }

    /* Store entry timestamp so the return probe can compute latency. */
    bpf_map_update_elem(&inflight_ioctls, &pid_tgid, &now, BPF_ANY);

    /*
     * Mark this tid as GPU-active so sched_switch knows to emit an event
     * if this thread gets preempted while inside the driver.
     */
    u8 active = 1;
    bpf_map_update_elem(&gpu_active_tids, &tid, &active, BPF_ANY);

    (void)cmd; /* used by kretprobe — silence unused warning */
    return 0;
}

/* ============================================================
 * KRETPROBE: nvidia_unlocked_ioctl (return)
 *
 * Fires when nvidia_unlocked_ioctl returns to its caller.
 * We compute latency and emit a ringbuf event only for:
 *   - slow ioctls (latency > WEDJAT_SLOW_IOCTL_NS)
 *   - failed ioctls (retval != 0)
 *
 * Everything else is already counted in ioctl_counts — no per-call event.
 * ============================================================ */
SEC("kretprobe/nvidia_unlocked_ioctl")
int BPF_KRETPROBE(handle_nvidia_ioctl_return, long retval)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 pid      = (u32)(pid_tgid >> 32);
    u32 tid      = (u32)(pid_tgid);
    u64 now      = bpf_ktime_get_ns();

    /* Remove GPU-active marker — thread is back in userspace. */
    bpf_map_delete_elem(&gpu_active_tids, &tid);

    /* Look up entry timestamp. */
    u64 *entry_ts = bpf_map_lookup_elem(&inflight_ioctls, &pid_tgid);
    if (!entry_ts) {
        /*
         * No entry record.  This can happen if:
         *   - the map was full at entry time
         *   - the probe was loaded after the ioctl started
         * Safe to skip — we just lose one latency measurement.
         */
        return 0;
    }

    u64 latency_ns = now - *entry_ts;
    bpf_map_delete_elem(&inflight_ioctls, &pid_tgid);

    /*
     * Only emit a ringbuf event for slow or failed ioctls.
     * Fast successful ioctls are already counted; a per-call event
     * would be a ringbuf firehose on active CUDA workloads.
     */
    if (latency_ns < WEDJAT_SLOW_IOCTL_NS && retval == 0)
        return 0;

    struct ioctl_event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
    if (!e)
        return 0;

    e->timestamp_ns = now;
    e->latency_ns   = latency_ns;
    e->pid          = pid;
    e->tid          = tid;
    e->retval       = (int)retval;
    e->cmd          = 0; /* cmd is not available in the return probe — */
                         /* would need a second inflight map to carry it */
    bpf_get_current_comm(&e->comm, sizeof(e->comm));

    bpf_ringbuf_submit(e, 0);
    return 0;
}

/* ============================================================
 * KPROBE: uvm_vm_fault_entry
 *
 * Fires when the NVIDIA UVM driver handles a page fault for managed memory
 * (cudaMallocManaged / __managed__ variables).  Each fault means a page
 * is being accessed that is not currently resident in the right memory
 * (CPU or GPU), and the UVM driver is migrating it.
 *
 * High fault rates are a performance problem — they indicate the application
 * is thrashing managed memory back and forth between CPU and GPU.
 *
 * Symbol: uvm_vm_fault_entry — found in /proc/kallsyms when nvidia-uvm.ko
 * is loaded.  Skip gracefully if absent (system without UVM or older driver).
 * ============================================================ */
SEC("kprobe/uvm_vm_fault_entry")
int BPF_KPROBE(handle_uvm_fault_entry)
{
    struct uvm_fault_event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
    if (!e)
        return 0;

    u64 pid_tgid    = bpf_get_current_pid_tgid();
    e->timestamp_ns = bpf_ktime_get_ns();
    e->pid          = (u32)(pid_tgid >> 32);
    e->tid          = (u32)(pid_tgid);
    bpf_get_current_comm(&e->comm, sizeof(e->comm));

    bpf_ringbuf_submit(e, 0);
    return 0;
}

/* ============================================================
 * KPROBE: uvm_migrate
 *
 * Fires when UVM migrates a range of managed memory between CPU and GPU.
 * Unlike uvm_vm_fault_entry (which handles individual page faults), this
 * covers explicit cudaMemPrefetchAsync or driver-initiated bulk migrations.
 *
 * Symbol stability: less stable than uvm_vm_fault_entry — check kallsyms.
 * ============================================================ */
SEC("kprobe/uvm_migrate")
int BPF_KPROBE(handle_uvm_migrate)
{
    struct uvm_migrate_event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
    if (!e)
        return 0;

    u64 pid_tgid    = bpf_get_current_pid_tgid();
    e->timestamp_ns = bpf_ktime_get_ns();
    e->pid          = (u32)(pid_tgid >> 32);
    e->tid          = (u32)(pid_tgid);
    bpf_get_current_comm(&e->comm, sizeof(e->comm));

    bpf_ringbuf_submit(e, 0);
    return 0;
}

/* ============================================================
 * TRACEPOINT: sched/sched_switch
 *
 * Fires every time the Linux scheduler switches from one task to another
 * on any CPU core.  This is a standard Linux kernel tracepoint — stable
 * ABI, always present, not NVIDIA-specific.
 *
 * We only care about switches where the outgoing task (prev) is a
 * GPU-active thread (tracked in gpu_active_tids).  Anything else we ignore
 * to avoid a firehose — sched_switch fires thousands of times per second.
 *
 * WHY THIS MATTERS:
 *   If a CUDA submitter thread is inside nvidia_unlocked_ioctl (or just
 *   submitted a kernel and is polling) and Linux preempts it, the GPU may
 *   sit idle waiting for the host thread to return.  This is one of the
 *   most common "why is my CUDA app slow" causes that is invisible from
 *   the GPU side.
 *
 * The tracepoint format for sched_switch:
 *   TP_PROTO(bool preempt,
 *            struct task_struct *prev,
 *            struct task_struct *next)
 *
 * BPF_PROG_TYPE_TRACEPOINT receives a raw tracepoint context.
 * We use the TP_STRUCT__entry fields via ctx directly.
 * ============================================================ */
SEC("tracepoint/sched/sched_switch")
int handle_sched_switch(struct trace_event_raw_sched_switch *ctx)
{
    /*
     * ctx->prev_pid is the tid of the outgoing task.
     * Check if it is in gpu_active_tids before doing any more work.
     * This keeps the fast path (non-GPU threads) near zero overhead.
     */
    u32 prev_tid = ctx->prev_pid;
    u8 *active = bpf_map_lookup_elem(&gpu_active_tids, &prev_tid);
    if (!active)
        return 0; /* not a GPU-active thread — ignore */

    struct preempt_event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
    if (!e)
        return 0;

    e->timestamp_ns = bpf_ktime_get_ns();
    e->prev_pid     = prev_tid;
    e->next_pid     = ctx->next_pid;

    /*
     * bpf_probe_read_kernel_str reads a kernel string safely.
     * ctx->prev_comm and ctx->next_comm are char[TASK_COMM_LEN] fields
     * in the tracepoint's format struct.
     */
    bpf_probe_read_kernel_str(&e->prev_comm, sizeof(e->prev_comm),
                               ctx->prev_comm);
    bpf_probe_read_kernel_str(&e->next_comm, sizeof(e->next_comm),
                               ctx->next_comm);

    bpf_ringbuf_submit(e, 0);
    return 0;
}
