/*
 * daemon/ebpf/kprobes/test/driver_kprobes_test.c
 *
 * Integration test for driver_kprobes.bpf.c — Layer 3 kernel probes.
 *
 * WHAT THIS FILE DOES:
 *   1. Loads driver_kprobes.bpf.o into the kernel (Level 1+2).
 *   2. Attaches kprobes/kretprobes/tracepoints:
 *        - kprobe  + kretprobe: nvidia_unlocked_ioctl
 *        - kprobe:              uvm_vm_fault_entry
 *        - kprobe:              uvm_migrate
 *        - tracepoint:          sched/sched_switch
 *   3. Forks and execs a CUDA fixture binary (k2 by default) as a child.
 *      k2 calls Driver API functions that trigger nvidia_unlocked_ioctl
 *      in the kernel.
 *   4. Polls the ring buffer while the child runs.
 *   5. Filters ioctl events to the child's PID.
 *   6. After the child exits, asserts (Level 4):
 *        - at least one ioctl event arrived from our child
 *        - the ioctl_counts map has a non-zero entry for the child's PID
 *
 * HOW THIS DIFFERS FROM uprobes/test:
 *   Uprobes attach to libcuda.so in userspace.
 *   Kprobes attach to symbols in the kernel / nvidia.ko module.
 *   Both tests fork+exec a CUDA fixture and read a ring buffer — same shape.
 *
 *   Important: nvidia_unlocked_ioctl fires for EVERY ioctl to the NVIDIA
 *   device, from ANY process.  We MUST filter on the child's PID to avoid
 *   asserting on other CUDA activity on the same machine.
 *
 * HOW TO BUILD (once driver_kprobes.skel.h exists):
 *   See test/Makefile.
 *
 * HOW TO RUN:
 *   sudo ./bin/driver_kprobes_test k2
 *   sudo ./bin/driver_kprobes_test k1
 *
 *   Requires root (CAP_BPF + CAP_PERFMON), a real GPU, and nvidia.ko loaded.
 *   k1/k2 must be built first: make -C ../../../../kernels_to_trace build
 *
 * CURRENT STATE:
 *   driver_kprobes.bpf.c is written but driver_kprobes.skel.h has not been
 *   generated yet.  The BPF loader/attach/ringbuf blocks are in TODO comments.
 *   The fork/exec/waitpid skeleton compiles today without libbpf.
 *
 * SYMBOL AVAILABILITY:
 *   nvidia_unlocked_ioctl is only present in /proc/kallsyms when nvidia.ko is
 *   loaded.  uvm_* symbols require nvidia-uvm.ko.  If either module is absent,
 *   the attach will fail.  The loader handles this by checking kallsyms first
 *   and printing a clear SKIP message rather than crashing.
 */

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

/*
 * When driver_kprobes.skel.h exists, uncomment:
 *
 * #include <bpf/libbpf.h>
 * #include "driver_kprobes.skel.h"
 * #include "../../wedjat_common.h"
 */

/* ============================================================
 * ASSERTION MACROS — identical across all test harnesses
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
 * EVENT STRUCTS (stubs until wedjat_common.h is filled in)
 *
 * Must match the layouts in driver_kprobes.bpf.c exactly.
 * ============================================================ */

struct ioctl_event {
    unsigned long long timestamp_ns;
    unsigned long long latency_ns;
    unsigned int       pid;
    unsigned int       tid;
    unsigned int       cmd;
    int                retval;
    char               comm[16];
};

struct uvm_fault_event {
    unsigned long long timestamp_ns;
    unsigned int       pid;
    unsigned int       tid;
    char               comm[16];
};

struct uvm_migrate_event {
    unsigned long long timestamp_ns;
    unsigned int       pid;
    unsigned int       tid;
    char               comm[16];
};

struct preempt_event {
    unsigned long long timestamp_ns;
    unsigned int       prev_pid;
    unsigned int       next_pid;
    char               prev_comm[16];
    char               next_comm[16];
};

/* ============================================================
 * KERNEL SYMBOL PROBE
 *
 * Check whether a kernel symbol is present before trying to attach.
 * Reads /proc/kallsyms line by line looking for the symbol name.
 *
 * Returns 1 if present, 0 if absent.
 *
 * Why: nvidia_unlocked_ioctl and uvm_* symbols only exist when the
 * NVIDIA/UVM kernel modules are loaded.  Failing to attach a kprobe
 * to a missing symbol is a hard error from libbpf — we check first
 * so we can print a clear SKIP message instead of a cryptic error.
 * ============================================================ */
static int kallsyms_has_symbol(const char *name)
{
    FILE *f = fopen("/proc/kallsyms", "r");
    if (!f)
        return 0;

    char line[256];
    int found = 0;
    while (fgets(line, sizeof(line), f)) {
        /*
         * kallsyms format:  <addr> <type> <name>[\t<module>]
         * We look for the symbol name as the third field.
         */
        char addr[64], type[4], sym[128];
        if (sscanf(line, "%63s %3s %127s", addr, type, sym) == 3) {
            if (strcmp(sym, name) == 0) {
                found = 1;
                break;
            }
        }
    }
    fclose(f);
    return found;
}

/* ============================================================
 * TEST STATE
 * ============================================================ */
static int   g_ioctl_events  = 0;  /* ioctl events from our child             */
static int   g_assert_failed = 0;  /* set to 1 if any per-event check fails   */
static pid_t g_fixture_pid   = -1;

/* ============================================================
 * RING BUFFER CALLBACK
 *
 * driver_kprobes.bpf.c emits different event types to the same ring
 * buffer.  In the real implementation, the event type would be
 * identified by an api_id field at a fixed offset.  For now the
 * callback shape is shown in comments.
 *
 * TODO: uncomment when skeleton exists.
 *
 * static int handle_event(void *ctx, void *data, size_t sz)
 * {
 *     (void)ctx;
 *
 *     // All events start with timestamp_ns + pid + tid at the same offsets.
 *     // Use api_id (once added to the structs) to dispatch to the right handler.
 *     struct ioctl_event *e = (struct ioctl_event *)data;
 *
 *     // Filter to our fixture child only.
 *     if (e->pid != (unsigned int)g_fixture_pid)
 *         return 0;
 *
 *     g_ioctl_events++;
 *
 *     // latency_ns should always be > 0 for a completed ioctl.
 *     if (e->latency_ns == 0) {
 *         fprintf(stderr, "FAIL: ioctl event %d has zero latency\n",
 *                 g_ioctl_events);
 *         g_assert_failed = 1;
 *     }
 *
 *     return 0;
 * }
 * ============================================================ */

/* ============================================================
 * FIXTURE RUNNER — identical to uprobes/test harnesses
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
 * TEST: nvidia_unlocked_ioctl kprobe fires when CUDA fixture runs
 * ============================================================ */
static int test_ioctl_kprobe_fires(const char *fixture_path)
{
    const int expected_iters = 3;

    printf("TEST ioctl_kprobe_fires fixture=%s iters=%d\n",
           fixture_path, expected_iters);

    /* -------------------------------------------------------
     * Symbol check: skip gracefully if nvidia.ko is not loaded.
     * ------------------------------------------------------- */
    if (!kallsyms_has_symbol("nvidia_unlocked_ioctl")) {
        printf("SKIP: nvidia_unlocked_ioctl not in /proc/kallsyms"
               " — nvidia.ko not loaded\n");
        return 0;
    }

    /* -------------------------------------------------------
     * Level 1+2: load BPF object.
     *
     * TODO: uncomment when skeleton exists.
     *
     * struct driver_kprobes_bpf *skel = driver_kprobes_bpf__open_and_load();
     * ASSERT_TRUE(skel != NULL, "failed to load driver_kprobes BPF skeleton");
     * ------------------------------------------------------- */

    /* -------------------------------------------------------
     * Level 3: attach kprobes.
     *
     * Unlike uprobes (which use attach_uprobe_opts), kprobes attach by
     * kernel symbol name.  The skeleton's auto-attach (host_ctx_bpf__attach)
     * handles this automatically when SEC("kprobe/symbol") is used.
     *
     * TODO: uncomment when skeleton exists.
     *
     * int err = driver_kprobes_bpf__attach(skel);
     * ASSERT_TRUE(err == 0, "failed to attach kprobes");
     *
     * // Open ring buffer.
     * struct ring_buffer *rb = ring_buffer__new(
     *     bpf_map__fd(skel->maps.events), handle_event, NULL, NULL);
     * ASSERT_TRUE(rb != NULL, "failed to open ring buffer");
     * ------------------------------------------------------- */

    /* Spawn the CUDA fixture. */
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
     * ring_buffer__poll(rb, 200);
     * ------------------------------------------------------- */

    int wstatus;
    if (waitpid(g_fixture_pid, &wstatus, 0) < 0) {
        perror("waitpid");
        return 1;
    }

    ASSERT_TRUE(WIFEXITED(wstatus),     "fixture did not exit normally");
    ASSERT_EQ(WEXITSTATUS(wstatus), 0,  "fixture exited with non-zero status");

    /* -------------------------------------------------------
     * Level 4: assert on captured events.
     *
     * k2 calls cuMemAlloc/cuStreamSynchronize which all go through
     * nvidia_unlocked_ioctl.  We expect at least one ioctl event
     * per fixture iteration.
     *
     * TODO: uncomment when skeleton exists.
     *
     * ASSERT_TRUE(!g_assert_failed,  "per-event assertions failed");
     * ASSERT_GE(g_ioctl_events, expected_iters,
     *           "expected at least one ioctl event per iteration");
     *
     * // Also check the fast-path counter map.
     * // The child's PID should have a non-zero ioctl count.
     * unsigned int child_pid = (unsigned int)g_fixture_pid;
     * u64 *count = NULL;
     * err = bpf_map__lookup_elem(skel->maps.ioctl_counts,
     *                            &child_pid, sizeof(child_pid),
     *                            &count, sizeof(count), 0);
     * ASSERT_TRUE(err == 0 && count && *count > 0,
     *             "ioctl_counts has no entry for fixture pid");
     * ------------------------------------------------------- */

    fprintf(stderr, "SKIP: BPF skeleton not generated yet — probe assertions not active\n");

    /* -------------------------------------------------------
     * Cleanup.
     *
     * TODO: uncomment when skeleton exists.
     *
     * ring_buffer__free(rb);
     * driver_kprobes_bpf__destroy(skel);
     * ------------------------------------------------------- */

    printf("SKIP ioctl_kprobe_fires (skeleton missing)\n");
    return 0;
}

/* ============================================================
 * ENTRY POINT
 *
 * Usage: sudo ./bin/driver_kprobes_test <fixture>
 *   Default fixture is k2 (Driver API: cuMemAlloc/cuStreamSynchronize).
 *   k1 also works — it still goes through nvidia_unlocked_ioctl.
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

    return test_ioctl_kprobe_fires(fixture_path);
}
