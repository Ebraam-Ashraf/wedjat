/*
 * daemon/ebpf/uprobes/test/cuda_actions_test.c
 *
 * Integration test for cuda_actions.bpf.c — the Driver API probes.
 *
 * WHAT THIS FILE DOES:
 *   1. Loads cuda_actions.bpf.o into the kernel (Level 1+2).
 *   2. Attaches uprobes/uretprobes to the Driver API functions that
 *      cuda_actions.bpf.c instruments:
 *        - cuMemAlloc / cuMemAlloc_v2   (entry + return)
 *        - cuMemcpyHtoDAsync            (entry)
 *        - cuStreamSynchronize          (entry + return)
 *   3. Forks and execs k2 (the CUDA Driver API fixture) as a child.
 *      k2 calls cuMemAlloc/cuMemcpyHtoDAsync/cuStreamSynchronize in a loop.
 *   4. Polls the ring buffer while the child runs.
 *   5. Filters events to the child's PID.
 *   6. After the child exits, asserts (Level 4):
 *        - at least WEDJAT_ITERS cuMemAlloc events arrived
 *        - every alloc event has bytes > 0
 *        - at least WEDJAT_ITERS cuStreamSynchronize events arrived
 *        - every sync event has a non-zero latency_ns
 *
 * HOW TO BUILD (once cuda_actions.skel.h exists):
 *   See test/Makefile.
 *
 * HOW TO RUN:
 *   sudo ./bin/cuda_actions_test k2
 *
 *   Requires root (CAP_BPF + CAP_PERFMON) and a real GPU.
 *   k2 must already be built: make -C ../../../../kernels_to_trace build
 *
 * CURRENT STATE:
 *   cuda_actions.bpf.c is design comments only — no real probes yet.
 *   The skeleton does not exist.  The BPF loader/attach/ringbuf blocks
 *   are in TODO comments below.  The fork/exec/waitpid skeleton compiles
 *   today without libbpf.
 *
 * RELATIONSHIP TO host_ctx_test.c:
 *   Identical shape.  The only differences are:
 *     - skeleton name:  cuda_actions_bpf  instead of host_ctx_bpf
 *     - attached symbols: cuMemAlloc, cuMemcpyHtoDAsync, cuStreamSynchronize
 *     - event struct:   action_event     instead of launch_event
 *     - default fixture: k2              instead of k1
 *     - assertions:     bytes/latency    instead of grid/block
 */

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

/*
 * When cuda_actions.skel.h exists, uncomment:
 *
 * #include <bpf/libbpf.h>
 * #include "cuda_actions.skel.h"
 * #include "../../wedjat_common.h"
 */

/* ============================================================
 * ASSERTION MACROS — identical to host_ctx_test.c
 * ============================================================ */

#define ASSERT_TRUE(cond, msg)                                          \
    do {                                                                \
        if (!(cond)) {                                                  \
            fprintf(stderr, "FAIL [%s:%d]: %s\n",                      \
                    __FILE__, __LINE__, (msg));                         \
            return 1;                                                   \
        }                                                               \
    } while (0)

#define ASSERT_EQ(actual, expected, msg)                                \
    do {                                                                \
        if ((actual) != (expected)) {                                   \
            fprintf(stderr, "FAIL [%s:%d]: %s (got %ld, want %ld)\n",  \
                    __FILE__, __LINE__, (msg),                          \
                    (long)(actual), (long)(expected));                  \
            return 1;                                                   \
        }                                                               \
    } while (0)

#define ASSERT_GE(actual, expected, msg)                                \
    do {                                                                \
        if ((actual) < (expected)) {                                    \
            fprintf(stderr, "FAIL [%s:%d]: %s (got %ld, want >= %ld)\n",\
                    __FILE__, __LINE__, (msg),                          \
                    (long)(actual), (long)(expected));                  \
            return 1;                                                   \
        }                                                               \
    } while (0)

/* ============================================================
 * EVENT STRUCT (stub until wedjat_common.h is filled in)
 *
 * Covers both alloc events and sync events.
 * The api_id field distinguishes which probe emitted the event.
 * Must match the layout in cuda_actions.bpf.c exactly.
 * ============================================================ */
struct action_event {
    unsigned long long timestamp_ns; /* when the entry probe fired           */
    unsigned long long latency_ns;   /* for return probes: time inside call  */
    unsigned long long bytes;        /* cuMemAlloc: requested size           */
    unsigned long long devptr;       /* cuMemAlloc return: device pointer    */
    unsigned int       pid;
    unsigned int       tid;
    unsigned int       api_id;       /* which CUDA function — see below      */
    unsigned int       result;       /* CUDA result code from return probe   */
    char               comm[16];
};

/*
 * api_id values — must match the constants in wedjat_common.h once defined.
 * Inline here as temporary stubs.
 */
#define API_CUDA_MEM_ALLOC       1
#define API_CUDA_MEMCPY_HTOD     2
#define API_CUDA_STREAM_SYNC     3

/* ============================================================
 * TEST STATE
 * ============================================================ */
static int   g_alloc_events  = 0;
static int   g_sync_events   = 0;
static int   g_assert_failed = 0;
static pid_t g_fixture_pid   = -1;

/* ============================================================
 * RING BUFFER CALLBACK
 *
 * TODO: uncomment when skeleton exists.
 *
 * static int handle_event(void *ctx, void *data, size_t sz)
 * {
 *     (void)ctx;
 *     if (sz < sizeof(struct action_event))
 *         return 0;
 *
 *     struct action_event *e = (struct action_event *)data;
 *
 *     // Ignore events from other processes on the machine.
 *     if (e->pid != (unsigned int)g_fixture_pid)
 *         return 0;
 *
 *     switch (e->api_id) {
 *     case API_CUDA_MEM_ALLOC:
 *         g_alloc_events++;
 *         if (e->bytes == 0) {
 *             fprintf(stderr, "FAIL: alloc event %d has bytes == 0\n",
 *                     g_alloc_events);
 *             g_assert_failed = 1;
 *         }
 *         break;
 *
 *     case API_CUDA_STREAM_SYNC:
 *         g_sync_events++;
 *         //
 *         // latency_ns is only known at the return probe.
 *         // The entry event arrives first with latency_ns == 0;
 *         // the return event arrives after with latency_ns set.
 *         // We only count events where latency is non-zero
 *         // (i.e. return events) to avoid double-counting.
 *         //
 *         if (e->latency_ns == 0)
 *             g_sync_events--; // entry event, not yet complete
 *         break;
 *
 *     default:
 *         break;
 *     }
 *
 *     return 0;
 * }
 * ============================================================ */

/* ============================================================
 * FIXTURE RUNNER — identical to host_ctx_test.c
 * ============================================================ */
static pid_t run_fixture(const char *binary_path, int iters, int sleep_ms)
{
    pid_t pid = fork();
    if (pid < 0) {
        perror("fork");
        return -1;
    }

    if (pid == 0) {
        char iters_str[16], sleep_str[16];
        snprintf(iters_str, sizeof(iters_str), "%d", iters);
        snprintf(sleep_str, sizeof(sleep_str), "%d", sleep_ms);
        setenv("WEDJAT_ITERS",    iters_str, 1);
        setenv("WEDJAT_SLEEP_MS", sleep_str, 1);

        execl(binary_path, binary_path, (char *)NULL);
        perror("execl");
        _exit(127);
    }

    return pid;
}

/* ============================================================
 * TEST FUNCTION
 * ============================================================ */
static int test_cuda_actions_driver_api(const char *fixture_path)
{
    /*
     * k2 calls cuMemAlloc once and cuStreamSynchronize once per iteration.
     * With WEDJAT_ITERS=3 we expect exactly 3 alloc events and 3 sync events.
     */
    const int expected_iters = 3;

    printf("TEST cuda_actions_driver_api fixture=%s iters=%d\n",
           fixture_path, expected_iters);

    /* -------------------------------------------------------
     * Level 1+2: load BPF object.
     *
     * TODO: uncomment when skeleton exists.
     *
     * struct cuda_actions_bpf *skel = cuda_actions_bpf__open_and_load();
     * ASSERT_TRUE(skel != NULL, "failed to load cuda_actions BPF skeleton");
     * ------------------------------------------------------- */

    /* -------------------------------------------------------
     * Level 3: attach uprobes/uretprobes.
     *
     * cuda_actions.bpf.c instruments multiple functions.
     * Each needs its own bpf_link.
     *
     * TODO: uncomment when skeleton exists.  Replace libcuda path
     * as needed for the target machine (ldconfig -p | grep libcuda).
     *
     * const char *libcuda = "/usr/lib/x86_64-linux-gnu/libcuda.so.1";
     *
     * // cuMemAlloc entry
     * struct bpf_uprobe_opts alloc_entry_opts = {
     *     .sz = sizeof(alloc_entry_opts),
     *     .func_name = "cuMemAlloc_v2",
     *     .retprobe  = false,
     * };
     * struct bpf_link *alloc_entry_link = bpf_program__attach_uprobe_opts(
     *     skel->progs.handle_cu_mem_alloc_entry, -1, libcuda,
     *     0, &alloc_entry_opts);
     * ASSERT_TRUE(alloc_entry_link != NULL,
     *     "failed to attach uprobe to cuMemAlloc_v2 entry");
     *
     * // cuMemAlloc return
     * struct bpf_uprobe_opts alloc_ret_opts = {
     *     .sz = sizeof(alloc_ret_opts),
     *     .func_name = "cuMemAlloc_v2",
     *     .retprobe  = true,
     * };
     * struct bpf_link *alloc_ret_link = bpf_program__attach_uprobe_opts(
     *     skel->progs.handle_cu_mem_alloc_return, -1, libcuda,
     *     0, &alloc_ret_opts);
     * ASSERT_TRUE(alloc_ret_link != NULL,
     *     "failed to attach uretprobe to cuMemAlloc_v2");
     *
     * // cuStreamSynchronize entry
     * struct bpf_uprobe_opts sync_entry_opts = {
     *     .sz = sizeof(sync_entry_opts),
     *     .func_name = "cuStreamSynchronize",
     *     .retprobe  = false,
     * };
     * struct bpf_link *sync_entry_link = bpf_program__attach_uprobe_opts(
     *     skel->progs.handle_cu_stream_sync_entry, -1, libcuda,
     *     0, &sync_entry_opts);
     * ASSERT_TRUE(sync_entry_link != NULL,
     *     "failed to attach uprobe to cuStreamSynchronize entry");
     *
     * // cuStreamSynchronize return
     * struct bpf_uprobe_opts sync_ret_opts = {
     *     .sz = sizeof(sync_ret_opts),
     *     .func_name = "cuStreamSynchronize",
     *     .retprobe  = true,
     * };
     * struct bpf_link *sync_ret_link = bpf_program__attach_uprobe_opts(
     *     skel->progs.handle_cu_stream_sync_return, -1, libcuda,
     *     0, &sync_ret_opts);
     * ASSERT_TRUE(sync_ret_link != NULL,
     *     "failed to attach uretprobe to cuStreamSynchronize");
     * ------------------------------------------------------- */

    /* -------------------------------------------------------
     * Open ring buffer reader.
     *
     * TODO: uncomment when skeleton exists.
     *
     * struct ring_buffer *rb = ring_buffer__new(
     *     bpf_map__fd(skel->maps.events), handle_event, NULL, NULL);
     * ASSERT_TRUE(rb != NULL, "failed to open ring buffer");
     * ------------------------------------------------------- */

    /* Spawn k2 as a child process. */
    g_fixture_pid = run_fixture(fixture_path, expected_iters, 20);
    ASSERT_TRUE(g_fixture_pid > 0, "failed to fork CUDA fixture");

    /* -------------------------------------------------------
     * Poll ring buffer while child runs.
     *
     * TODO: replace with real poll once skeleton exists.
     *
     * int wstatus;
     * while (waitpid(g_fixture_pid, &wstatus, WNOHANG) == 0)
     *     ring_buffer__poll(rb, 100);
     * ring_buffer__poll(rb, 200);  // final drain
     * ------------------------------------------------------- */

    int wstatus;
    if (waitpid(g_fixture_pid, &wstatus, 0) < 0) {
        perror("waitpid");
        return 1;
    }

    /* Assert the CUDA fixture itself exited cleanly. */
    ASSERT_TRUE(WIFEXITED(wstatus),       "fixture did not exit normally");
    ASSERT_EQ(WEXITSTATUS(wstatus), 0,    "fixture exited with non-zero status");

    /* -------------------------------------------------------
     * Level 4: assert on captured events.
     *
     * TODO: uncomment when skeleton exists.
     *
     * ASSERT_TRUE(!g_assert_failed,
     *     "per-event assertions failed");
     * ASSERT_EQ(g_alloc_events, expected_iters,
     *     "wrong number of cuMemAlloc events");
     * ASSERT_EQ(g_sync_events, expected_iters,
     *     "wrong number of cuStreamSynchronize events");
     * ------------------------------------------------------- */

    fprintf(stderr, "SKIP: BPF skeleton not generated yet — probe assertions not active\n");

    /* -------------------------------------------------------
     * Cleanup.
     *
     * TODO: uncomment when skeleton exists.
     *
     * ring_buffer__free(rb);
     * bpf_link__destroy(sync_ret_link);
     * bpf_link__destroy(sync_entry_link);
     * bpf_link__destroy(alloc_ret_link);
     * bpf_link__destroy(alloc_entry_link);
     * cuda_actions_bpf__destroy(skel);
     * ------------------------------------------------------- */

    printf("SKIP cuda_actions_driver_api (skeleton missing)\n");
    return 0;
}

/* ============================================================
 * ENTRY POINT
 *
 * Usage: sudo ./bin/cuda_actions_test <fixture>
 *   Default fixture is k2 (CUDA Driver API calls).
 *   k1 can also be passed but only triggers cuLaunchKernel,
 *   not the memory/sync probes this test focuses on.
 * ============================================================ */
int main(int argc, char *argv[])
{
    if (argc < 2) {
        fprintf(stderr, "usage: %s <fixture>\n", argv[0]);
        fprintf(stderr, "  <fixture> is k1, k2, etc. from kernels_to_trace/build/\n");
        fprintf(stderr, "  example:  sudo %s k2\n", argv[0]);
        return 2;
    }

    char fixture_path[512];
    snprintf(fixture_path, sizeof(fixture_path),
             "../../../../kernels_to_trace/build/%s", argv[1]);

    if (access(fixture_path, X_OK) != 0) {
        fprintf(stderr, "FAIL: fixture not found or not executable: %s\n",
                fixture_path);
        fprintf(stderr, "  run: make -C kernels_to_trace build\n");
        return 2;
    }

    return test_cuda_actions_driver_api(fixture_path);
}
