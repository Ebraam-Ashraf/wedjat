/* poller_test.c
 *
 * Integration test for poller.c — Layer 4 device and process stats.
 *
 * Structure:
 *   1. environment check  (GPU visible via poller_init)
 *   2. device snapshot    (sanity-range asserts on device 0)
 *   3. process snapshot   (spawn CUDA fixture, poll until pid appears)
 *
 * usage:  ./bin/poller_test <fixture>   (k1, k2, … from kernels_to_trace/build)
 * env:    USE_NVML=1  (set by Makefile when running real binary)
 * exit:   0 PASS  1 FAIL  77 SKIP
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/wait.h>
#include <signal.h>

#include "../poller.c"

#define PASS 0
#define FAIL 1
#define SKIP 77

/* ── assertion macros ────────────────────────────────────────────────────── */

#define ASSERT_TRUE(cond, msg)                                          \
    do {                                                                \
        if (!(cond)) {                                                  \
            fprintf(stderr, "FAIL [%s:%d]: %s\n",                      \
                    __FILE__, __LINE__, (msg));                         \
            return FAIL;                                                \
        }                                                               \
    } while (0)

#define ASSERT_IN_RANGE(val, lo, hi, msg)                               \
    do {                                                                \
        if ((long long)(val) < (long long)(lo) ||                      \
            (long long)(val) > (long long)(hi)) {                      \
            fprintf(stderr,                                             \
                    "FAIL [%s:%d]: %s (got %lld, want [%lld, %lld])\n",\
                    __FILE__, __LINE__, (msg),                          \
                    (long long)(val), (long long)(lo), (long long)(hi));\
            return FAIL;                                                \
        }                                                               \
    } while (0)

/* ── helpers ─────────────────────────────────────────────────────────────── */

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

/* ── tests ───────────────────────────────────────────────────────────────── */

static int test_device_snapshot(void)
{
    printf("TEST device_snapshot\n");

    struct device_snapshot snap;
    poller_snapshot_device(0, &snap);

    if (!snap.valid) {
        printf("SKIP: poller_snapshot_device not available (nvml_error=%d)\n",
               snap.nvml_error);
        return SKIP;
    }

    ASSERT_IN_RANGE(snap.gpu_util, 0, 100, "gpu_util must be 0-100");
    ASSERT_IN_RANGE(snap.mem_util, 0, 100, "mem_util must be 0-100");
    ASSERT_IN_RANGE(snap.temp_c,   0, 150, "temp_c must be 0-150 C");
    ASSERT_TRUE(snap.mem_total > 0,         "mem_total must be > 0");
    ASSERT_TRUE(snap.mem_used <= snap.mem_total, "mem_used must not exceed mem_total");
    ASSERT_TRUE(snap.uuid[0] != '\0',       "uuid must be non-empty");

    printf("PASS device_snapshot"
           " (gpu=%u%% temp=%u°C mem=%llu/%lluMiB uuid=%.20s...)\n",
           snap.gpu_util, snap.temp_c,
           snap.mem_used / (1024*1024), snap.mem_total / (1024*1024),
           snap.uuid);
    return PASS;
}

static int test_process_snapshot(const char *fixture_path)
{
    printf("TEST process_snapshot fixture=%s\n", fixture_path);

    /* 256 MiB: large enough to clear driver overhead, fits any modern GPU. */
    const long alloc_bytes = 256L * 1024L * 1024L;
    const long n_elements  = alloc_bytes / (long)sizeof(float);
    const int  max_polls   = 20;

    pid_t child = run_fixture(fixture_path, n_elements, 10, 300);
    ASSERT_TRUE(child > 0, "failed to fork CUDA fixture");

    const struct process_entry *found = NULL;
    struct process_snapshot snap;

    for (int i = 0; i < max_polls && !found; i++) {
        poller_snapshot_processes(0, &snap);
        if (snap.valid)
            found = poller_find_pid(&snap, (unsigned int)child);
        if (!found)
            usleep(150 * 1000);
    }

    kill(child, SIGTERM);
    int wstatus;
    waitpid(child, &wstatus, 0);

    if (!snap.valid) {
        printf("SKIP: poller_snapshot_processes not available (nvml_error=%d)\n",
               snap.nvml_error);
        return SKIP;
    }

    ASSERT_TRUE(found != NULL,
                "fixture pid never appeared in NVML compute process list");

    long long used = (long long)found->used_gpu_memory;
    ASSERT_IN_RANGE(used, alloc_bytes / 2, alloc_bytes * 4,
                    "usedGpuMemory should be plausible for the known allocation");

    printf("PASS process_snapshot (pid=%d used=%lldMiB alloc=%ldMiB)\n",
           child, used / (1024*1024), alloc_bytes / (1024*1024));
    return PASS;
}

/* ── main ────────────────────────────────────────────────────────────────── */

int main(int argc, char *argv[])
{
    if (argc < 2) {
        fprintf(stderr, "usage: %s <fixture>  (e.g. k1)\n", argv[0]);
        return FAIL;
    }

    char fixture_path[512];
    snprintf(fixture_path, sizeof(fixture_path),
             "../../../kernels_to_trace/build/%s", argv[1]);

    if (access(fixture_path, X_OK) != 0) {
        fprintf(stderr, "FAIL: fixture not found or not executable: %s\n"
                        "  run: make -C kernels_to_trace build\n", fixture_path);
        return FAIL;
    }

    printf("== poller_test ==\n");

    /* 1. environment */
    if (poller_init() != 0) {
        printf("SKIP: poller_init failed — no GPU or NVML unavailable\n");
        return SKIP;
    }

    /* 2. device snapshot */
    int rc = test_device_snapshot();
    if (rc == FAIL) { poller_shutdown(); return FAIL; }

    /* 3. process snapshot */
    rc = test_process_snapshot(fixture_path);

    poller_shutdown();

    printf("\npoller_test: %s\n", rc == PASS ? "PASS" : (rc == SKIP ? "SKIP" : "FAIL"));
    return rc;
}
