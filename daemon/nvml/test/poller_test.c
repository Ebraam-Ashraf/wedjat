/*
 * daemon/nvml/test/poller_test.c
 *
 * Integration test for daemon/nvml/poller.c — Layer 4 device and process stats.
 *
 * WHAT THIS FILE DOES:
 *   1. Calls the global NVML stat functions directly (no fixture needed):
 *        - poller_snapshot_device(0, ...)
 *        - asserts: utilization 0–100, temp 0–150°C, mem_total > 0
 *      These "sanity range" assertions are all we can do — we cannot predict
 *      exact values for hardware state we don't control.
 *
 *   2. Spawns a CUDA fixture (k1 by default) as a child with a known
 *      allocation size (WEDJAT_N controls it), then polls
 *      poller_snapshot_processes() until the child's PID appears.
 *        - asserts: child PID is found within the timeout
 *        - asserts: reported usedGpuMemory is in the right ballpark for
 *          the known allocation (factor-of-2 slack for driver/context overhead)
 *
 * WHY THE RANGE ASSERTION:
 *   NVML's usedGpuMemory includes driver overhead you do not control.
 *   An exact-byte assertion would be fragile across driver versions.
 *   The range check (allocation / 2 ... allocation * 4) confirms NVML is
 *   reporting the right process and a plausible value, not a tight match.
 *
 * HOW TO BUILD (once poller.c NVML calls are uncommented):
 *   See test/Makefile.
 *   gcc -o bin/poller_test poller_test.c ../poller.c \
 *       -I/usr/local/cuda/include -lnvidia-ml
 *
 * HOW TO RUN:
 *   ./bin/poller_test k1
 *   ./bin/poller_test k2
 *
 *   Does NOT require root.  Does require a real GPU and nvidia-smi working.
 *
 * CURRENT STATE:
 *   poller.c functions return -1 / leave structs zeroed (not implemented yet).
 *   The harness structure is complete; real assertions are in TODO comments.
 *   The fork/exec/waitpid skeleton is real and compiles today.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/wait.h>
#include <signal.h>

/* Pull in the structs and function signatures from poller.c. */
#include "../poller.c"

/* ============================================================
 * ASSERTION MACROS — identical across all Wedjat test harnesses
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

#define ASSERT_IN_RANGE(val, lo, hi, msg)                               \
    do {                                                                \
        if ((val) < (lo) || (val) > (hi)) {                            \
            fprintf(stderr,                                             \
                    "FAIL [%s:%d]: %s (got %lld, want [%lld, %lld])\n",\
                    __FILE__, __LINE__, (msg),                          \
                    (long long)(val), (long long)(lo), (long long)(hi));\
            return 1;                                                   \
        }                                                               \
    } while (0)

/* ============================================================
 * FIXTURE RUNNER
 * ============================================================ */
static pid_t run_fixture(const char *path, long n, int iters, int sleep_ms)
{
    pid_t pid = fork();
    if (pid < 0) { perror("fork"); return -1; }

    if (pid == 0) {
        char n_str[32], iters_str[16], sleep_str[16];
        snprintf(n_str,     sizeof(n_str),     "%ld", n);
        snprintf(iters_str, sizeof(iters_str), "%d",  iters);
        snprintf(sleep_str, sizeof(sleep_str), "%d",  sleep_ms);
        setenv("WEDJAT_N",        n_str,     1);
        setenv("WEDJAT_ITERS",    iters_str, 1);
        setenv("WEDJAT_SLEEP_MS", sleep_str, 1);

        execl(path, path, (char *)NULL);
        perror("execl");
        _exit(127);
    }
    return pid;
}

/* ============================================================
 * TEST 1: global device snapshot sanity
 *
 * No fixture needed.  Just calls poller_snapshot_device() and checks
 * that the returned values are in plausible hardware ranges.
 * ============================================================ */
static int test_device_snapshot_sanity(void)
{
    printf("TEST device_snapshot_sanity\n");

    struct wedjat_device_snapshot snap;
    poller_snapshot_device(0, &snap);

    if (!snap.valid) {
        fprintf(stderr, "SKIP: poller_snapshot_device not implemented yet"
                " (nvml_error=%d)\n", snap.nvml_error);
        return 0;
    }

    /* Utilization is a percentage — must be 0–100. */
    ASSERT_IN_RANGE(snap.gpu_util, 0, 100,
                    "gpu_util should be a valid percentage");
    ASSERT_IN_RANGE(snap.mem_util, 0, 100,
                    "mem_util should be a valid percentage");

    /* Temperature: a GPU below 0°C or above 150°C is a sensor error. */
    ASSERT_IN_RANGE(snap.temp_c, 0, 150,
                    "temperature reading should be plausible (0–150°C)");

    /* Total VRAM must be > 0 if the device exists. */
    ASSERT_TRUE(snap.mem_total > 0,
                "mem_total should be > 0 for a real GPU");

    /* Used <= total is a basic sanity check. */
    ASSERT_TRUE(snap.mem_used <= snap.mem_total,
                "mem_used should not exceed mem_total");

    /* UUID must be non-empty. */
    ASSERT_TRUE(snap.uuid[0] != '\0',
                "device UUID should be non-empty");

    printf("PASS device_snapshot_sanity"
           " (gpu=%u%% temp=%u°C mem_used=%lluMB/%lluMB uuid=%.20s...)\n",
           snap.gpu_util, snap.temp_c,
           snap.mem_used / (1024*1024), snap.mem_total / (1024*1024),
           snap.uuid);
    return 0;
}

/* ============================================================
 * TEST 2: process snapshot — known PID appears with plausible memory
 *
 * Spawns a CUDA fixture with a known allocation size (256 MiB of floats).
 * Polls poller_snapshot_processes() until the child's PID shows up or
 * a timeout expires.  Asserts the reported usedGpuMemory is in the right
 * ballpark.
 * ============================================================ */
static int test_process_snapshot_finds_fixture(const char *fixture_path)
{
    /*
     * 256 MiB allocation: large enough to show up clearly above driver
     * overhead, small enough to fit in any modern GPU.
     * WEDJAT_N = bytes / sizeof(float) = 256M / 4 = 67108864 elements.
     */
    const long   alloc_bytes  = 256L * 1024L * 1024L;
    const long   n_elements   = alloc_bytes / (long)sizeof(float);
    const int    iters        = 10;
    const int    sleep_ms     = 300; /* stay alive long enough to poll */
    const int    max_attempts = 20;

    printf("TEST process_snapshot_finds_fixture fixture=%s alloc=%ldMiB\n",
           fixture_path, alloc_bytes / (1024*1024));

    if (poller_init() != 0) {
        fprintf(stderr, "SKIP: poller_init failed — NVML not implemented yet\n");
        return 0;
    }

    /* Spawn the CUDA fixture. */
    pid_t child = run_fixture(fixture_path, n_elements, iters, sleep_ms);
    ASSERT_TRUE(child > 0, "failed to fork CUDA fixture");

    /* -------------------------------------------------------
     * Poll until child appears in the process list or timeout.
     *
     * nvmlDeviceGetComputeRunningProcesses_v3 only reports a PID if it
     * is actively using the GPU right now.  There is a small window
     * between fork() and the first cuMalloc call where it will not
     * appear yet — hence the retry loop.
     * ------------------------------------------------------- */
    const struct wedjat_process_entry *found_entry = NULL;
    struct wedjat_process_snapshot snap;

    for (int attempt = 0; attempt < max_attempts && !found_entry; attempt++) {
        poller_snapshot_processes(0, &snap);

        if (snap.valid) {
            found_entry = poller_find_pid(&snap, (unsigned int)child);
        }

        if (!found_entry)
            usleep(150 * 1000); /* 150 ms between polls */
    }

    /* Child has been observed (or timed out) — signal it to exit now. */
    kill(child, SIGTERM);
    int wstatus;
    waitpid(child, &wstatus, 0);

    if (!snap.valid) {
        fprintf(stderr, "SKIP: poller_snapshot_processes not implemented yet\n");
        poller_shutdown();
        return 0;
    }

    /* Level 4 assertions. */
    ASSERT_TRUE(found_entry != NULL,
                "fixture PID never appeared in NVML compute process list");

    /*
     * usedGpuMemory range check:
     *   lower bound = alloc_bytes / 2   (we're seeing at least half)
     *   upper bound = alloc_bytes * 4   (driver overhead is at most 3x our alloc)
     *
     * Do not tighten this — driver overhead varies across versions and
     * configurations.  The point is "NVML saw a plausible amount for this
     * process," not an exact byte match.
     */
    long long used = (long long)found_entry->used_gpu_memory;
    ASSERT_IN_RANGE(used,
                    alloc_bytes / 2,
                    alloc_bytes * 4,
                    "usedGpuMemory should be in plausible range for the known allocation");

    poller_shutdown();

    printf("PASS process_snapshot_finds_fixture"
           " (pid=%d usedGpuMemory=%lldMiB alloc=%ldMiB)\n",
           child,
           used / (1024*1024),
           alloc_bytes / (1024*1024));
    return 0;
}

/* ============================================================
 * ENTRY POINT
 *
 * Usage: ./bin/poller_test <fixture>
 *   <fixture> is k1, k2, etc. from kernels_to_trace/build/
 *   example:  ./bin/poller_test k1
 * ============================================================ */
int main(int argc, char *argv[])
{
    if (argc < 2) {
        fprintf(stderr, "usage: %s <fixture>\n", argv[0]);
        fprintf(stderr, "  <fixture> is k1, k2, etc. from kernels_to_trace/build/\n");
        fprintf(stderr, "  example:  %s k1\n", argv[0]);
        return 2;
    }

    char fixture_path[512];
    snprintf(fixture_path, sizeof(fixture_path),
             "../../../kernels_to_trace/build/%s", argv[1]);

    if (access(fixture_path, X_OK) != 0) {
        fprintf(stderr, "FAIL: fixture not found or not executable: %s\n",
                fixture_path);
        fprintf(stderr, "  run: make -C kernels_to_trace build\n");
        return 2;
    }

    int rc = 0;
    rc |= test_device_snapshot_sanity();
    rc |= test_process_snapshot_finds_fixture(fixture_path);
    return rc;
}
