/*
 * daemon/ebpf/uprobes/host_ctx.bpf.c
 *
 * Layer 1 + Layer 2: host process identity + CUDA kernel-launch capture.
 *
 * WHAT THIS FILE IS:
 *   This is an eBPF program that runs inside the Linux kernel.  It attaches
 *   to userspace functions in libcuda.so (uprobes) and fires every time one
 *   of those functions is called by any process on the system.
 *
 *   It is NOT a normal C program.  It is compiled to BPF bytecode by clang,
 *   then loaded into the kernel by the test harness (host_ctx_test.c) or the
 *   production daemon.  The kernel verifier checks it before it ever runs.
 *
 * LAYER 1 — who called CUDA?
 *   pid, tid, comm, cgroup_id, cpu_id, timestamp.
 *   These fields answer "which Linux process/thread made this call and when."
 *
 * LAYER 2 — what did they ask CUDA to do?
 *   grid/block dimensions, shared memory size, stream handle.
 *   These fields answer "what GPU work was requested."
 *
 * WHY TWO LAYERS IN ONE FILE HERE:
 *   cuLaunchKernel is the best first proof-of-concept hook because:
 *     a) almost every CUDA workload calls it
 *     b) its arguments carry both identity (from the calling thread) and
 *        intent (grid/block shape)
 *   The more complex Layer 2 probes (cuMemAlloc, cuStreamSynchronize, etc.)
 *   live in cuda_actions.bpf.c once basic launch tracing works.
 *
 * ATTACHMENT:
 *   The SEC() name is generic ("uprobe/cuLaunchKernel") because the real
 *   library path (libcuda.so.1, libcuda.so.550.xx, etc.) is not known at
 *   compile time.  The loader resolves it at runtime.  See host_ctx_test.c
 *   for how bpf_program__attach_uprobe_opts() does the attachment.
 */

#include "vmlinux.h"           /* kernel type definitions (generated once per kernel) */
#include <bpf/bpf_helpers.h>   /* bpf_map_lookup_elem, bpf_ringbuf_reserve, etc.    */
#include <bpf/bpf_tracing.h>   /* BPF_UPROBE macro, PT_REGS_PARM*                   */
#include "../wedjat_common.h"  /* shared structs/constants between BPF and userspace */

char LICENSE[] SEC("license") = "Dual BSD/GPL";

/* ============================================================
 * MAPS
 *
 * A BPF map is a kernel-resident data structure that both the BPF program
 * (running in the kernel) and userspace (the test harness / daemon) can
 * read and write.  Think of it as shared memory between kernel and user.
 * ============================================================ */

/*
 * events — ring buffer for full structured events (slow path).
 *
 * Used for: complete launch_event records that the test harness reads
 * to assert pid/comm/grid shape.
 *
 * Ring buffers are preferred over perf buffers for new code because:
 *   - memory-efficient: fixed kernel-side buffer, consumer controls pace
 *   - ordered: events come out in timestamp order
 *   - no per-CPU duplication
 *
 * max_entries is the total byte capacity.  256 KiB holds ~2730 launch
 * events at sizeof(struct launch_event) == 96 bytes.  Increase if the
 * ring fills up under heavy workloads (bpf_ringbuf_reserve returns NULL
 * when full — the probe handles that by dropping the event, not crashing).
 */
struct {
    __uint(type,        BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 256 * 1024);
} events SEC(".maps");

/*
 * launch_counts — per-CPU hash map of pid -> launch count (fast path).
 *
 * Used for: lightweight counting without a ringbuf entry per launch.
 * Per-CPU means each CPU core has its own copy of the value, which
 * eliminates spinlock contention.  The daemon sums across CPUs when
 * it wants a total count.
 *
 * max_entries is the max number of distinct PIDs tracked simultaneously.
 * 1024 is more than enough for typical GPU workloads.
 */
struct {
    __uint(type,        BPF_MAP_TYPE_PERCPU_HASH);
    __uint(max_entries, 1024);
    __type(key,         u32);  /* pid */
    __type(value,       u64);  /* launch count */
} launch_counts SEC(".maps");

/* ============================================================
 * EVENT STRUCT
 *
 * This struct is written by the BPF program and read by userspace.
 * Both sides MUST agree on the layout — any difference causes silent
 * data corruption.  The canonical definition belongs in wedjat_common.h
 * once that header is filled in; it is inline here for now so the file
 * compiles independently.
 * ============================================================ */
struct launch_event {
    u64  timestamp_ns; /* bpf_ktime_get_ns(): nanoseconds since boot       */
    u32  pid;          /* thread group id = Linux PID as seen by userspace  */
    u32  tid;          /* thread id = Linux TID                             */
    char comm[16];     /* process name, same as /proc/<pid>/comm            */
    u32  grid_x;       /* gridDimX passed to cuLaunchKernel                 */
    u32  grid_y;       /* gridDimY                                           */
    u32  grid_z;       /* gridDimZ                                           */
    u32  block_x;      /* blockDimX                                          */
    u32  block_y;      /* blockDimY                                          */
    u32  block_z;      /* blockDimZ                                          */
    u32  _pad;         /* explicit padding to keep 8-byte alignment          */
};

/* ============================================================
 * UPROBE: cuLaunchKernel
 *
 * cuLaunchKernel C signature (CUDA Driver API):
 *
 *   CUresult cuLaunchKernel(
 *       CUfunction  f,              // arg 1: kernel function handle
 *       unsigned int gridDimX,      // arg 2
 *       unsigned int gridDimY,      // arg 3
 *       unsigned int gridDimZ,      // arg 4
 *       unsigned int blockDimX,     // arg 5
 *       unsigned int blockDimY,     // arg 6
 *       unsigned int blockDimZ,     // arg 7
 *       unsigned int sharedMemBytes,// arg 8
 *       CUstream     hStream,       // arg 9
 *       void       **kernelParams,  // arg 10
 *       void       **extra          // arg 11
 *   );
 *
 * BPF_UPROBE (defined in bpf_tracing.h) generates a probe function that
 * pulls each argument off pt_regs according to the platform ABI (on x86-64:
 * rdi, rsi, rdx, rcx, r8, r9, then stack).  You just name the parameters
 * and the macro handles the register mapping — no PT_REGS_PARM* calls needed.
 *
 * We declare only the arguments we care about (up to blockDimZ = arg 7).
 * Arguments after that (sharedMem, stream, kernelParams, extra) are ignored
 * here; they belong in the Layer 2 cuda_actions.bpf.c once this works.
 * ============================================================ */
SEC("uprobe/cuLaunchKernel")
int BPF_UPROBE(handle_cu_launch_kernel,
               void *f,           /* arg 1 — kernel function handle (ignored here) */
               u32 gridDimX,      /* arg 2 */
               u32 gridDimY,      /* arg 3 */
               u32 gridDimZ,      /* arg 4 */
               u32 blockDimX,     /* arg 5 */
               u32 blockDimY,     /* arg 6 */
               u32 blockDimZ)     /* arg 7 */
{
    /*
     * bpf_get_current_pid_tgid() returns a u64 packed as:
     *   upper 32 bits = tgid (= PID as seen by userspace)
     *   lower 32 bits = tid  (= thread ID)
     */
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 pid = (u32)(pid_tgid >> 32);
    u32 tid = (u32)(pid_tgid);

    /* -------------------------------------------------------
     * Fast path: update per-CPU launch counter for this pid.
     *
     * We try to increment an existing entry first.  If none
     * exists yet, we insert with initial value 1.
     *
     * __sync_fetch_and_add is the BPF-safe atomic add —
     * required because multiple CPU cores may update the same
     * map entry concurrently.
     * ------------------------------------------------------- */
    u64 *count = bpf_map_lookup_elem(&launch_counts, &pid);
    if (count) {
        __sync_fetch_and_add(count, 1);
    } else {
        u64 init = 1;
        bpf_map_update_elem(&launch_counts, &pid, &init, BPF_ANY);
    }

    /* -------------------------------------------------------
     * Slow path: emit a full structured event to the ring buffer.
     *
     * bpf_ringbuf_reserve allocates space inside the ring buffer
     * for one event and returns a pointer to it.  The event is
     * NOT visible to userspace yet — we fill it in, then call
     * bpf_ringbuf_submit to make it visible.
     *
     * If the ring buffer is full, reserve returns NULL.  We drop
     * the event silently rather than blocking — the counter above
     * still got incremented, so the drop is detectable.
     * ------------------------------------------------------- */
    struct launch_event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
    if (!e)
        return 0; /* ring full — drop this event, counter already updated */

    e->timestamp_ns = bpf_ktime_get_ns();
    e->pid          = pid;
    e->tid          = tid;
    e->grid_x       = gridDimX;
    e->grid_y       = gridDimY;
    e->grid_z       = gridDimZ;
    e->block_x      = blockDimX;
    e->block_y      = blockDimY;
    e->block_z      = blockDimZ;
    e->_pad         = 0;

    /*
     * bpf_get_current_comm fills comm with the task's name
     * (same as /proc/<pid>/comm, max 16 bytes including '\0').
     */
    bpf_get_current_comm(&e->comm, sizeof(e->comm));

    /*
     * bpf_ringbuf_submit makes the event visible to userspace.
     * After this call, `e` must not be accessed again.
     */
    bpf_ringbuf_submit(e, 0);
    return 0;
}
