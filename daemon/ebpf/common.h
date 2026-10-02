#ifndef WEDJAT_COMMON_H
#define WEDJAT_COMMON_H

#ifdef __bpf__
/* vmlinux.h (when used) already provides all kernel types and defines
 * __VMLINUX_H__.  In that case skip the system bpf headers to avoid
 * redefinition collisions.  When vmlinux.h is NOT used (e.g. older
 * out-of-tree builds), pull in the system headers as before.          */
#ifndef __VMLINUX_H__
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#endif
#else  /* userspace — plain C test/loader files */
#include <linux/types.h>
typedef __u32 u32;
typedef __u64 u64;
typedef __s32 s32;
#endif

/* Device ordinals come from the CUDA process, not from NVML. They are relative
 * to CUDA_VISIBLE_DEVICES and must be resolved to a physical UUID in userspace
 * before being persisted as a GPU identity. */
#define WEDJAT_UNKNOWN_DEVICE ((u32)0xffffffffU)

/* Event IDs are a compact wire contract. Keep existing values stable. */
enum event_id {
    EVENT_CTX_SET        = 1,
    EVENT_CTX_CREATE     = 2,
    EVENT_CTX_DESTROY    = 3,
    EVENT_LAUNCH         = 4,
    EVENT_ALLOC          = 5,
    EVENT_FREE           = 6,
    EVENT_MEMCPY         = 7,
    EVENT_SYNC           = 8,
    EVENT_UVM_FAULT      = 9,
    EVENT_UVM_MIGRATE    = 10,
    EVENT_UVM_EVICT      = 11,
    EVENT_IOCTL          = 12,
    EVENT_MMAP           = 13,
    EVENT_SM_BLOCK_START = 14,
    EVENT_SM_BLOCK_END   = 15,
    EVENT_PROC_EXEC      = 16,
    EVENT_PROC_EXIT      = 17,
    EVENT_UVM_IOCTL      = 18,
    EVENT_CTX_POP        = 19,
    EVENT_ID_MAX
};

enum event_flags {
    EVENT_F_NONE              = 0,
    EVENT_F_DEVICE_UNKNOWN    = 1U << 0,
    EVENT_F_FROM_PID_FALLBACK = 1U << 1,
    EVENT_F_RAW_CAPTURE       = 1U << 2
};

/*
 * Host-to-userspace event format, intentionally exactly 64 bytes.
 *
 * device_ordinal is never an NVML or physical GPU index. For normal events
 * start_boottime_ns is zero. For process lifecycle events it identifies the
 * process even if /proc disappears before the daemon handles the record.
 */
struct event {
    u64 ts_ns;
    u64 start_boottime_ns;
    u64 latency_ns;
    u64 address;
    u64 bytes;
    u32 tgid;
    u32 tid;
    u32 device_ordinal;
    u32 api_id;
    u32 flags;
    s32 status;
};
_Static_assert(sizeof(struct event) == 64, "event ABI must remain 64 bytes");

/* A thread ID is globally unique while alive, but pid_tgid makes ownership
 * explicit and lets userspace remove all state belonging to one process. */
struct thread_key {
    u64 pid_tgid;
};

/* CUDA context pointers are process virtual addresses, not machine-global IDs. */
struct ctx_key {
    u32 tgid;
    u32 pad;
    u64 ctx;
};

struct inflight_key {
    u64 pid_tgid;
    u32 api_id;
    u32 pad;
};

struct inflight_val {
    u64 start_ts_ns;
    u64 arg1;
    u64 arg2;
    u32 device_ordinal;
    u32 flags;
};

struct alloc_key {
    u32 tgid;
    u32 pad;
    u64 address;
};

struct alloc_val {
    u64 bytes;
    u32 device_ordinal;
    u32 pad;
    u64 start_boottime_ns;
};

struct process_seen_val {
    u64 start_boottime_ns;
};

/* Pinned per-process state carries a generation token because TGIDs are
 * reused, including while the daemon is down. */
struct device_binding {
    u32 device_ordinal;
    u32 pad;
    u64 start_boottime_ns;
};

struct agg_key {
    u32 tgid;
    u32 device_ordinal;
    u32 api_id;
    u32 pad;
};

/* Values are per CPU. Sum counters, but take the maximum for latency_max_ns. */
struct agg_val {
    u64 count;
    u64 bytes;
    u64 latency_sum_ns;
    u64 latency_max_ns;
    u64 alloc_bytes;
    u64 free_bytes;
    u64 errors;
    u64 uvm_faults;
    u64 uvm_evicts;
};

/* One stats slot per event ID, plus slot zero for global counters. */
struct stats_val {
    u64 ringbuf_drops;
    u64 map_update_failures;
    u64 unknown_device_events;
    u64 alloc_free_misses;
};

struct config_val {
    u32 flags;
    u32 sync_stall_us;
};

enum stat_id {
    STAT_RINGBUF_DROP = 0,
    STAT_MAP_UPDATE_FAILURE = 1,
    STAT_UNKNOWN_DEVICE = 2,
    STAT_ALLOC_FREE_MISS = 3
};

enum config_flags {
    CONFIG_F_RAW_CAPTURE         = 1U << 0,
    CONFIG_F_AGGREGATE_HOT_PATHS = 1U << 1
};

/* The single mangled CUDA kernel both gpu_sm probes are bound to. Kept here
 * so the SEC() names, the test's symbol-presence check, and the docs cannot
 * drift apart. A fixture that does not contain this symbol legitimately
 * produces no events; one that does must produce them. */
#define WEDJAT_TARGET_SYM "_Z16scale_add_kernelPfffi"

/* Record sent from the GPU to userspace, one per thread block at kernel entry
 * and exit. ts_ns is the GPU timer, not the host clock. */
struct dev_event {
    u32 api_id;
    u32 sm_id;
    u32 ctaid_x;
    u32 ctaid_y;
    u32 ctaid_z;
    u32 pid;
    u64 ts_ns;
};
_Static_assert(sizeof(struct dev_event) == 32, "device event ABI must remain 32 bytes");

#if defined(__bpf__) && !defined(DEVICE_BPF)
#include <bpf/bpf_core_read.h>

/* Rare, important records. Hot paths update agg_map unless raw capture is on. */
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 256 * 1024);
} events_pipe SEC(".maps");

/* Separate bounded LRU maps prevent cross-probe pairing collisions and let
 * abandoned calls age out after process death. */
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, struct inflight_key);
    __type(value, struct inflight_val);
} ctx_inflight_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, struct inflight_key);
    __type(value, struct inflight_val);
} cuda_inflight_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, struct inflight_key);
    __type(value, struct inflight_val);
} driver_inflight_map SEC(".maps");

/* Pinned state maps include process start time to reject stale state on PID reuse. */
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 10240);
    __type(key, struct thread_key);
    __type(value, struct device_binding);
} tid_to_device SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 16384);
    __type(key, u32);
    __type(value, struct device_binding);
} pid_to_device SEC(".maps");

/* Pinned state map: a context pointer is scoped by its owning process. */
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 4096);
    __type(key, struct ctx_key);
    __type(value, struct device_binding);
} ctx_to_device SEC(".maps");

/* Pinned state map. It is used for alloc/free counters, never VRAM truth. */
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 32768);
    __type(key, struct alloc_key);
    __type(value, struct alloc_val);
} alloc_map SEC(".maps");

/* Per-second hot-path accounting. Batch-drain with lookup-and-delete. */
struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_HASH);
    __uint(max_entries, 8192);
    __uint(map_flags, BPF_F_NO_PREALLOC);
    __type(key, struct agg_key);
    __type(value, struct agg_val);
} agg_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, EVENT_ID_MAX);
    __type(key, u32);
    __type(value, struct stats_val);
} stats_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, u32);
    __type(value, struct config_val);
} config_map SEC(".maps");

/* Tracks only processes that have produced GPU telemetry. */
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 16384);
    __type(key, u32);
    __type(value, struct process_seen_val);
} seen_processes SEC(".maps");

static __always_inline struct inflight_key make_inflight_key(u64 pid_tgid, u32 api_id)
{
    struct inflight_key key = { .pid_tgid = pid_tgid, .api_id = api_id };
    return key;
}

static __always_inline struct thread_key current_thread_key(void)
{
    struct thread_key key = {
        .pid_tgid = bpf_get_current_pid_tgid(),
    };
    return key;
}

static __always_inline u32 current_device_ordinal(void)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    struct thread_key key = { .pid_tgid = pid_tgid };
    u32 tgid = (u32)(pid_tgid >> 32);
    struct device_binding *device = bpf_map_lookup_elem(&tid_to_device, &key);
    struct task_struct *task = (struct task_struct *)bpf_get_current_task_btf();
    u64 start_boottime_ns = BPF_CORE_READ(task, start_boottime);

    if (device) {
        if (device->start_boottime_ns == start_boottime_ns)
            return device->device_ordinal;
        bpf_map_delete_elem(&tid_to_device, &key);
    }
    device = bpf_map_lookup_elem(&pid_to_device, &tgid);
    if (device) {
        if (device->start_boottime_ns == start_boottime_ns)
            return device->device_ordinal;
        bpf_map_delete_elem(&pid_to_device, &tgid);
    }
    return WEDJAT_UNKNOWN_DEVICE;
}

static __always_inline void init_event(struct event *event, u32 api_id)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = (u32)(pid_tgid >> 32);
    struct thread_key tkey = { .pid_tgid = pid_tgid };
    struct device_binding *tid_dev = bpf_map_lookup_elem(&tid_to_device, &tkey);

    __builtin_memset(event, 0, sizeof(*event));
    event->ts_ns = bpf_ktime_get_ns();
    event->tgid = tgid;
    event->tid = (u32)pid_tgid;
    event->api_id = api_id;

    struct task_struct *task = (struct task_struct *)bpf_get_current_task_btf();
    u64 start_boottime_ns = BPF_CORE_READ(task, start_boottime);
    if (tid_dev && tid_dev->start_boottime_ns == start_boottime_ns) {
        event->device_ordinal = tid_dev->device_ordinal;
    } else {
        if (tid_dev)
            bpf_map_delete_elem(&tid_to_device, &tkey);
        struct device_binding *pid_dev = bpf_map_lookup_elem(&pid_to_device, &tgid);
        if (pid_dev) {
            if (pid_dev->start_boottime_ns == start_boottime_ns) {
                event->device_ordinal = pid_dev->device_ordinal;
                event->flags |= EVENT_F_FROM_PID_FALLBACK;
            } else {
                bpf_map_delete_elem(&pid_to_device, &tgid);
                event->device_ordinal = WEDJAT_UNKNOWN_DEVICE;
                event->flags |= EVENT_F_DEVICE_UNKNOWN;
            }
        } else {
            event->device_ordinal = WEDJAT_UNKNOWN_DEVICE;
            event->flags |= EVENT_F_DEVICE_UNKNOWN;
        }
    }
}

static __always_inline void stats_add(u32 api_id, enum stat_id stat_id)
{
    u32 slot = api_id < EVENT_ID_MAX ? api_id : 0;
    struct stats_val *stats = bpf_map_lookup_elem(&stats_map, &slot);

    if (!stats)
        return;
    if (stat_id == STAT_RINGBUF_DROP)
        __sync_fetch_and_add(&stats->ringbuf_drops, 1);
    else if (stat_id == STAT_MAP_UPDATE_FAILURE)
        __sync_fetch_and_add(&stats->map_update_failures, 1);
    else if (stat_id == STAT_UNKNOWN_DEVICE)
        __sync_fetch_and_add(&stats->unknown_device_events, 1);
    else if (stat_id == STAT_ALLOC_FREE_MISS)
        __sync_fetch_and_add(&stats->alloc_free_misses, 1);
}

static __always_inline void mark_gpu_process(u32 tgid)
{
    struct task_struct *task;
    struct process_seen_val value = {};

    /* BPF_NOEXIST makes the insert atomic — no separate lookup needed. */
    if (bpf_map_lookup_elem(&seen_processes, &tgid))
        return;
    task = (struct task_struct *)bpf_get_current_task_btf();
    value.start_boottime_ns = BPF_CORE_READ(task, start_boottime);
    if (bpf_map_update_elem(&seen_processes, &tgid, &value, BPF_NOEXIST) != 0) {
        /* EEXIST just means another CPU raced us — not an error. */
        if (!bpf_map_lookup_elem(&seen_processes, &tgid))
            stats_add(0, STAT_MAP_UPDATE_FAILURE);
    }
}

static __always_inline int submit_event(struct event *event)
{
    /* EVENT_F_DEVICE_UNKNOWN is already set by init_event when appropriate.
     * Proc lifecycle events are exempt — they intentionally have no device. */
    if (event->device_ordinal == WEDJAT_UNKNOWN_DEVICE &&
        event->api_id != EVENT_PROC_EXEC && event->api_id != EVENT_PROC_EXIT)
        stats_add(event->api_id, STAT_UNKNOWN_DEVICE);
    mark_gpu_process(event->tgid);
    if (bpf_ringbuf_output(&events_pipe, event, sizeof(*event), 0) != 0) {
        stats_add(event->api_id, STAT_RINGBUF_DROP);
        return 0;
    }
    return 1;
}

static __always_inline int record_aggregate(struct event *event)
{
    u32 zero = 0;
    struct config_val *config = bpf_map_lookup_elem(&config_map, &zero);
    u32 flags = config ? config->flags : CONFIG_F_AGGREGATE_HOT_PATHS;
    struct agg_key key = {
        .tgid = event->tgid,
        .device_ordinal = event->device_ordinal,
        .api_id = event->api_id,
    };
    struct agg_val initial = {};
    struct agg_val *value;

    mark_gpu_process(event->tgid);
    if (flags & CONFIG_F_RAW_CAPTURE) {
        event->flags |= EVENT_F_RAW_CAPTURE;
        return submit_event(event);
    }
    if (event->device_ordinal == WEDJAT_UNKNOWN_DEVICE)
        stats_add(event->api_id, STAT_UNKNOWN_DEVICE);

    /* Try inserting a zero-initialised slot. If the key already exists the
     * update returns an error, which is the normal hot path — ignore it and
     * fall through to the lookup below. Only treat it as a real failure if
     * the subsequent lookup also finds nothing (map full). */
    bpf_map_update_elem(&agg_map, &key, &initial, BPF_NOEXIST);
    value = bpf_map_lookup_elem(&agg_map, &key);
    if (!value) {
        stats_add(event->api_id, STAT_MAP_UPDATE_FAILURE);
        return 0;
    }
    value->count++;
    if (event->status == 0)
        value->bytes += event->bytes;
    value->latency_sum_ns += event->latency_ns;
    if (event->latency_ns > value->latency_max_ns)
        value->latency_max_ns = event->latency_ns;
    if (event->status != 0)
        value->errors++;
    if (event->status == 0 && event->api_id == EVENT_ALLOC)
        value->alloc_bytes += event->bytes;
    else if (event->status == 0 && event->api_id == EVENT_FREE)
        value->free_bytes += event->bytes;
    else if (event->api_id == EVENT_UVM_FAULT)
        value->uvm_faults++;
    else if (event->api_id == EVENT_UVM_EVICT)
        value->uvm_evicts++;
    return 1;
}

#endif // host maps


#if defined(__bpf__) && defined(DEVICE_BPF)

// GPU -> userspace ring buffer (bpftime GPU ringbuf map type, 1527)
// key/value must be declared: libbpf only fills those in automatically for the
// kernel's own ringbuf type, so without them this map loads with size 0.
//
// max_entries is the depth of ONE per-GPU-thread ring and bpftime allocates
// one ring per thread (BPFTIME_MAP_GPU_THREAD_COUNT), so the footprint is
// max_entries * thread_count * (value_size + 8). At 16 x 8192 x 40B that is
// 5MB, allocated in the *loading* process via cuMemHostGetDevicePointer
// (map_handler.cpp, nv_gpu_ringbuf_map.cpp). That process later forks the CUDA
// workloads, and CUDA contexts do not survive fork() — see the test README,
// which measures the failure mode. 5MB is fine; the test's children execve
// rather than running CUDA post-fork.
//
// The ring is indexed by *global* thread id (getGlobalThreadId in bpftime's
// default_trampoline.cu), so thread_count must be >= WEDJAT_MAX_GLOBAL_TID in
// gpu_sm.bpf.c, which is what caps the number of instrumented blocks. Keep
// the two in sync with BPFTIME_MAP_GPU_THREAD_COUNT in the test Makefile.
// 8192 / 256 threads per block covers all 32 blocks of k1; a larger launch
// would need a ring indexed by something other than global thread id.
struct {
    __uint(type, 1527);
    __uint(max_entries, 16);
    __type(key, u32);
    __type(value, struct dev_event);
} dev_events_pipe SEC(".maps");

#endif // device maps

#endif // WEDJAT_COMMON_H
