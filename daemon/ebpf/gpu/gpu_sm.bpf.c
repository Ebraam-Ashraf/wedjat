/*
 * daemon/ebpf/gpu/gpu_sm.bpf.c
 *
 * Layer 5: per-SM block placement, running inside the GPU.
 *
 * Not loaded into the Linux kernel: bpftime JITs this to PTX and injects it
 * into the target CUDA kernel. "kprobe"/"kretprobe" here mean CUDA kernel
 * entry/exit, and the symbol is the mangled CUDA kernel name.
 *
 * bpftime decides a program is a GPU program by
 * bpftime_prog::is_cuda(), which is name.starts_with("cuda__") on the
 * eBPF *function* name (runtime/include/bpftime_prog.hpp). Miss the prefix
 * on the function and attach is dropped with only a SPDLOG_DEBUG, so the test
 * sees no events and no error.
 *
 * The symbol after "kprobe/" must NOT carry the cuda_ prefix. bpftime only
 * strips that prefix when BPFTIME_RUN_WITH_KERNEL is set
 * (syscall_context.cpp:856), and the pass matches the kernel name exactly
 * (find_kernel_body, `m[2] == kernel`) against the real PTX entry
 * `.entry _Z16scale_add_kernelPfffi`. With the prefix left on, the pass looks
 * for a kernel that does not exist and patches nothing. So: prefix on the
 * function, plain mangled name in the section.
 *
 * Only thread (0,0,0) of each block reports, once at entry and once at exit.
 * The daemon pairs the two records by ctaid and groups them by sm_id.
 *
 * bpf_get_sm_id is helper 509 (reads %smid) — stock bpftime already has it.
 * Helper 507 looks like a candidate but is asm("exit;"), which kills the
 * thread before the event is written.
 *
 * The symbol in both SEC lines is k1's scale_add_kernel. It has to be the
 * mangled name of a kernel the workload actually launches, or every row in
 * gpu_sm_test prints n/a:
 *   cuobjdump -symbols kernels_to_trace/build/k1 | grep scale_add
 *   k2: _Z11tiled_sgemmPKfS0_Pfiiiff   k3: _Z11reduce_passPKfPfl
 *
 * "Block end" is approximate: the exit probe runs when thread 0 finishes,
 * not when the whole block does, so durations are a lower bound for blocks
 * with several warps.
 *
 * Build:
 *   clang -O2 -g -target bpf -D__TARGET_ARCH_x86 -I. -c gpu_sm.bpf.c -o gpu_sm.bpf.o
 */
#define DEVICE_BPF 1

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "common.h"

char LICENSE[] SEC("license") = "Dual BSD/GPL";

// bpftime GPU helpers, from attach/nv_attach_impl/trampoline/default_trampoline.cu
// 501 puts  502 globaltimer  503 blockIdx  504 blockDim  505 threadIdx
// 506 membar  507 exit (!)  508 gridDim  509 smid
// 509 is the smid one — do NOT use 507, it is asm("exit;") and kills the
// thread before the event is ever written.
static u64  (*bpf_get_globaltimer)(void)                    = (void *)502;
static long (*bpf_get_block_idx)(u64 *x, u64 *y, u64 *z)    = (void *)503;
static long (*bpf_get_block_dim)(u64 *x, u64 *y, u64 *z)    = (void *)504;
static long (*bpf_get_thread_idx)(u64 *x, u64 *y, u64 *z)   = (void *)505;
static u64  (*bpf_get_sm_id)(void)                          = (void *)509;

// How many blocks report. bpftime's GPU ringbuf is indexed by *global* thread
// id (blockIdx*blockDim+threadIdx, see getGlobalThreadId in
// default_trampoline.cu), and the perf_event_output helper silently drops a
// write whose global id is >= BPFTIME_MAP_GPU_THREAD_COUNT. Thread 0 of block b
// has global id b*blockDim.x, so with k1's 256-wide blocks a 4096-block launch
// spans ~1M global ids — instrumenting all of them is not practical.
// We therefore instrument only blocks whose global id is under this limit,
// and the test must set BPFTIME_MAP_GPU_THREAD_COUNT >= this value.
// At 256 threads/block this covers WEDJAT_MAX_GLOBAL_TID/256 blocks.
#define WEDJAT_MAX_GLOBAL_TID 8192

static __always_inline bool wedjat_report_this_block(u64 *ctaid_x)
{
    u64 bx, by, bz, bd[3], td[3];
    bpf_get_thread_idx(&td[0], &td[1], &td[2]);
    /* one report per block: thread (0,0,0) only */
    if (td[0] || td[1] || td[2])
        return false;
    bpf_get_block_idx(&bx, &by, &bz);
    bpf_get_block_dim(&bd[0], &bd[1], &bd[2]);
    *ctaid_x = bx;
    return bx * bd[0] < WEDJAT_MAX_GLOBAL_TID;
}

// kernel entry
SEC("kprobe/" WEDJAT_TARGET_SYM)
int cuda__trace_sm_block(void)
{
    u64 bx = 0;
    if (!wedjat_report_this_block(&bx))
        return 0;

    struct dev_event e = {};
    e.api_id  = EVENT_SM_BLOCK_START;
    e.sm_id   = bpf_get_sm_id();
    e.ctaid_x = bx;
    e.ctaid_y = 0;
    e.ctaid_z = 0;
    e.pid     = bpf_get_current_pid_tgid() >> 32;
    e.ts_ns   = bpf_get_globaltimer();

    bpf_perf_event_output(NULL, &dev_events_pipe, 0, &e, sizeof(e));
    return 0;
}

// kernel exit
SEC("kretprobe/" WEDJAT_TARGET_SYM)
int cuda__trace_sm_block_ret(void)
{
    u64 bx = 0;
    if (!wedjat_report_this_block(&bx))
        return 0;

    struct dev_event e = {};
    e.api_id  = EVENT_SM_BLOCK_END;
    e.sm_id   = bpf_get_sm_id();
    e.ctaid_x = bx;
    e.ctaid_y = 0;
    e.ctaid_z = 0;
    e.pid     = bpf_get_current_pid_tgid() >> 32;
    e.ts_ns   = bpf_get_globaltimer();

    bpf_perf_event_output(NULL, &dev_events_pipe, 0, &e, sizeof(e));
    return 0;
}