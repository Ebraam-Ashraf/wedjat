// /*



// Everything goes in `common.h`, and the `.bpf.c` files contain only programs.

// ## 1. Enums

// | Enum | Values (examples) |
// |---|---|
// | `api_id` | launch, alloc, free, memcpy_htod, memcpy_dtoh, memcpy_dtod, stream_sync, ctx_sync, ctx_set, ioctl, uvm_fault, uvm_migrate, uvm_evict, sched_preempt |
// | `event_type` | error, slow_alloc, slow_sync, slow_ioctl, preempt_stall |

// ## 2. Structs

// | Struct | Fields | Used by |
// |---|---|---|
// | `agg_key` | `cgroup_id`, `pid`, `api_id`, `device_id`, `_pad` | `agg_map` key |
// | `agg_val` | `calls`, `bytes`, `latency_ns`, `errors`, `max_latency_ns` | `agg_map` value |
// | `event` | `ts_ns`, `cgroup_id`, `bytes`, `latency_ns`, `pid`, `tid`, `device_id`, `api_id`, `event_type`, `error_code`, `comm[16]` | `events` ringbuf record |
// | `inflight_key` | `pid_tgid`, `api_id`, `_pad` | `inflight` key |
// | `inflight_val` | `start_ts_ns`, `arg` (bytes, or a saved pointer like `pctx`) | `inflight` value |

// Add later, when you build those features: `alloc_key` / `alloc_val` (leak tracking) and `func_key` / `func_val` (kernel names).

// ## 3. Maps

// | Map | Type | Key → Value | max_entries |
// |---|---|---|---|
// | `tid_to_device` | HASH | `u32 tid` → `u32 device` | 10240 |
// | `ctx_to_device` | HASH | `u64 ctx` → `u32 device` | 4096 |
// | `agg_map` | PERCPU_HASH | `agg_key` → `agg_val` | 10240 |
// | `events` | RINGBUF | (queue of `event`) | 256 KB |
// | `inflight` | HASH | `inflight_key` → `inflight_val` | 4096 |
// | `offcpu_start` | HASH | `u32 tid` → `u64 ts` | 10240 |

// Add later: `alloc_map`, `func_names`.

// ## 4. The skeleton

// ```c
// #ifndef COMMON_H
// #define COMMON_H
// /*

// /* ---- enums + structs: seen by BPF code AND user space ---- 
// enum api_id { /* ... */ };
// enum event_type { /* ... */ };

// struct agg_key      { /* ... */ };
// struct agg_val      { /* ... */ };
// struct event        { /* ... */ };
// struct inflight_key { /* ... */ };
// struct inflight_val { /* ... */ };

// /* ---- maps: only when compiling BPF code ---- */
// #ifdef __bpf__
// struct { /* ... */ } tid_to_device SEC(".maps");
// struct { /* ... */ } ctx_to_device SEC(".maps");
// struct { /* ... */ } agg_map       SEC(".maps");
// struct { /* ... */ } events        SEC(".maps");
// struct { /* ... */ } inflight      SEC(".maps");
// struct { /* ... */ } offcpu_start  SEC(".maps");
// #endif

// #endif
// ```

// ## Two consequences of "everything in common.h"

// **1. Every object creates every map.** `host_ctx.bpf.o` will also create `agg_map`, `inflight` and the rest even though it never uses them. Hash maps allocate their memory at creation, and PERCPU ones multiply it by the CPU count, so this wastes real memory. The loader fixes it before loading:

// ```c
// bpf_map__set_autocreate(skel->maps.agg_map, false);   // this object doesn't need it
// ```

// Only do that for maps a given object doesn't use.

// **2. The loader still has to connect the shared ones.** Three copies of `tid_to_device`, `agg_map` and `events` exist (one per object). Use `bpf_map__reuse_fd()` so all three objects point at one real copy. The others (`ctx_to_device`, `inflight`, `offcpu_start`) are used by one object each, so they need no connecting. If you don't want the `set_autocreate` calls, you could also keep private maps out of `common.h`, but you've decided against that, so this is the price.

// Do you want me to fill in the `common.h` fields next?


// */

/*
#include<vmlinux.h>
#include<bfp/bpf_helpers.h>
#include<bpf/trace_helpers.h>
#include"/home/ebraam/wedjat/daemon/ebpf/common.h"


struct my_value { int x, y, z; };

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __type(key, int);
    __type(value, struct my_value);
    __uint(max_entries, 16);
} icmpcnt SEC(".maps");

bpf_map__set_autocreate();
*/











//===================================================//

#ifndef WEDJAT_COMMON_H
#define WEDJAT_COMMON_H

/* =========================================================
* Types — compatible with both clang BPF and userspace gcc
* ========================================================= */
#ifdef __bpf__
typedef unsigned int        u32;
typedef unsigned long long  u64;
typedef int                 s32;
#else
#include <linux/types.h>
typedef __u32 u32;
typedef __u64 u64;
typedef __s32 s32;
#endif
#include<vmlinux.h>
#include<bfp/bpf_helpers.h>
#include<bpf/trace_helpers.h>
/* =========================================================
* ENUM: api_id
* One value per probe group. Tests use these names directly.
* ========================================================= */
enum api_id {
    /* cuda_actions.bpf.c */
    API_LAUNCH       = 1,
    API_ALLOC        = 2,
    API_FREE         = 3,
    API_MEMCPY_HTOD  = 4,
    API_MEMCPY_DTOH  = 5,
    API_MEMCPY_DTOD  = 6,
    API_STREAM_SYNC  = 7,
    API_CTX_SYNC     = 8,
    /* host_ctx.bpf.c */
    API_CTX_SET      = 9,
    /* driver_kprobes.bpf.c */
    API_UVM_FAULT    = 10,
    API_UVM_MIGRATE  = 11,
    API_UVM_EVICT    = 12,
    API_IOCTL        = 13,
    /* future — slots reserved, not implemented yet */
    API_GPU_BLOCK    = 14,  /* bpftime Layer 5 */
    API_NVML         = 15,  /* NVML poller */
};

/* =========================================================
* ENUM: event_type
* Used in struct event to say WHY this record was emitted.
* ========================================================= */
enum event_type {
    EVENT_INFO       = 0,   /* normal call, emitted for debugging */
    EVENT_SLOW_SYNC  = 1,   /* sync stall exceeded threshold */
    EVENT_OOM        = 2,   /* CUDA alloc returned error */
    EVENT_UVM_THRASH = 3,   /* same address faulting in a tight loop */
    EVENT_HW_FAULT   = 4,   /* Xid error from driver */
};

/* =========================================================
* STRUCT: agg_key
* Key for agg_map. Tests filter by .pid and .api_id.
* ========================================================= */
struct agg_key {
    u64 cgroup_id;
    u32 pid;
    u32 api_id;
    u32 device_id;
    u32 _pad;       /* 8-byte alignment for PERCPU_HASH */
};

/* =========================================================
* STRUCT: agg_val
* Value for agg_map. Tests read all 5 fields.
* ========================================================= */
struct agg_val {
    u64 calls;
    u64 bytes;
    u64 latency_ns;
    u64 errors;
    u64 max_latency_ns;
};

/* =========================================================
* STRUCT: inflight_val
* Saved at entry probe, read at exit probe.
* Key is u64 pid_tgid (unique per thread, no compound key needed).
* ========================================================= */
struct inflight_val {
    u64 start_ts_ns;
    u64 arg;        /* bytes for alloc/memcpy; ctx pointer for ctx calls */
};

/* =========================================================
* STRUCT: event
* Ringbuf record. Only emitted for anomalies and slow paths.
* sm_id and block_id are 0 for all Layer 1-4 events.
* ========================================================= */
struct event {
    u64 ts_ns;
    u64 latency_ns;
    u64 address;        /* alloc'd ptr or faulting UVM virtual address */
    u64 bytes;
    u32 pid;
    u32 tid;
    u32 device_id;
    u32 api_id;
    u32 event_type;
    s32 status_code;    /* CUresult or kernel errno */
    u32 sm_id;          /* Layer 5 only, 0 otherwise */
    u32 block_id;       /* Layer 5 only, 0 otherwise */
    char comm[16];      /* bpf_get_current_comm() */
};

/* =========================================================
* MAPS — kernel side only
* ========================================================= */
#ifdef __bpf__
#include <bpf/bpf_helpers.h>

/* Latency tracking: entry saves here, exit reads + deletes */
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key,   u64);                  /* pid_tgid */
    __type(value, struct inflight_val);
} inflight_map SEC(".maps");

/* Thread → GPU (written by host_ctx, read by cuda_actions + kprobes) */
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key,   u32);                  /* tid */
    __type(value, u32);                  /* device_id */
} tid_to_device SEC(".maps");

/* CUDA context ptr → GPU (internal, only host_ctx writes this) */
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key,   u64);                  /* CUcontext pointer */
    __type(value, u32);                  /* device_id */
} ctx_to_device SEC(".maps");

/* High-frequency aggregation — stays in kernel until main.c reads it */
struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_HASH);
    __uint(max_entries, 10240);
    __type(key,   struct agg_key);
    __type(value, struct agg_val);
} agg_map SEC(".maps");

/* Slow-path raw events — OOM, UVM thrash, errors only */
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 256 * 1024);
} events SEC(".maps");

#endif /* __bpf__ */

#endif /* WEDJAT_COMMON_H */