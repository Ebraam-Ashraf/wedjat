#ifndef WEDJAT_COMMON_H
#define WEDJAT_COMMON_H

#ifdef __bpf__
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#endif

// i think it's easier rather that strings, in Go part will map it 
enum event_id {
    EVENT_CTX_SET      = 1,
    EVENT_CTX_CREATE   = 2,
    EVENT_CTX_DESTROY  = 3,
    EVENT_LAUNCH       = 4,
    EVENT_ALLOC        = 5,
    EVENT_FREE         = 6,
    EVENT_MEMCPY       = 7,
    EVENT_SYNC         = 8,
    EVENT_UVM_FAULT    = 9,
    EVENT_UVM_MIGRATE  = 10,
    EVENT_UVM_EVICT    = 11,
    EVENT_IOCTL        = 12,
    EVENT_MMAP         = 13
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

#ifdef __bpf__

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


#endif // __bpf__

#endif // WEDJAT_COMMON_H