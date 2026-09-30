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

// i think it's easier rather that strings, in Go part will map it
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
    EVENT_SM_BLOCK_END   = 15
};

struct event {
    u32 api_id; // from the enum
    u64 ts_ns;
    u64 latency_ns;
    u32 pid;
    u32 tid;
    u32 device_id;
    u64 address;
    u64 bytes;
    s32 status;
};

struct inflight_val {
    u64 start_ts_ns;
    u64 arg1;
};

/* The single mangled CUDA kernel both gpu_sm probes are bound to. Kept here
 * so the SEC() names, the test's symbol-presence check, and the docs cannot
 * drift apart. A fixture that does not contain this symbol legitimately
 * produces no events; one that does must produce them. */
#define WEDJAT_TARGET_SYM "_Z16scale_add_kernelPfffi"


// record sent from the GPU to userspace, one per thread block at kernel entry and exit
// ts_ns is the GPU timer, not the host clock
struct dev_event {
    u32 api_id;   // EVENT_SM_BLOCK_START or EVENT_SM_BLOCK_END
    u32 sm_id;    // physical SM (%smid)
    u32 ctaid_x;
    u32 ctaid_y;
    u32 ctaid_z;
    u32 pid;
    u64 ts_ns;
};

#if defined(__bpf__) && !defined(DEVICE_BPF)

// events map to for userspace to store on db
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 256 * 1024);
} events_pipe SEC(".maps");


// used by all entry/exit probes to calculate latency
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
    __type(key,   u32);                  // tid (Thread ID)
    __type(value, struct inflight_val);  // start time
} inflight_map SEC(".maps");


/*
============== Why 2 maps for device_id? ==============

problem:
    we wanna know a thread now where is it running now ? on which GPU device_id ?
    not all apis like cuMemAlloc() can hook from it device_id

why not PID -> device_id?
    A single cpu process can use multiple GPUs

why not TID -> device_id only?
    Threads can dynamically switch GPUs

why not CTX -> device_id only?
    as we know one program can have many processes
    each process has many ctx objects per GPU
    and process each has many threads
    threads can dynamically switch GPUs
    and we wanna know witch thread on which gpu now
    +
    not all apis like cuMemAlloc() has ctx

solution:
    ctx_to_device (The Setup Map)
        on initialization calls cuCtxCreate(&ctx, flags, device_id) and save [Context 0xABC -> GPU 1].

    tid_to_device (The Runtime Router Map)
        When a worker thread binds to a GPU, it calls cuCtxSetCurrent(0xABC).
        We intercept this, query our ctx_to_device map with 0xABC, and it answers "GPU 1".
        We then map the current executing thread: [TID -> GPU 1].


think with the complex scenario:
    one program many cpu processes
    one of them many cpu threads
    one cpu thread and we have many GPUs
    theads can be dynamically switch GPUs
    [A single CPU thread can never be bound to more that one GPU simultaneously and on switch ]

currently is:
    tid-> device_id just a performance wise
    better is tid->ctx
    but for now i dont see any need to know ctx
    ai says :
    1. NVIDIA MPS (Multi-Process Service) & Multi-Tenancy
    2. Memory Leak Tracking
    latter tbd but for now im fine with the 2 maps solution
    maybe migrate to
        one is the tid->{ctx,deviceid}
        and another ctx->device_id
 */

// key : TID
// value : device_id
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key,   u32);
    __type(value, u32);
} tid_to_device SEC(".maps");

// key : ctx
// value : device_id
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key,   u64);
    __type(value, u32);
} ctx_to_device SEC(".maps");


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