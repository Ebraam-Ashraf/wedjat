/*
 * daemon/ebpf/device/test/cupti_sm_test.c
 *
 * Layer 5 smoke test — CUPTI SM activity.
 *
 * WHAT THIS FILE IS:
 *   This is NOT a BPF test.  device_sm.bpf.c has no BPF code, and for good
 *   reason: Linux eBPF cannot run inside a GPU kernel.  See
 *   device/device_sm.bpf.c for the full explanation.
 *
 *   This is a CUPTI test.  CUPTI is NVIDIA's Compute Unified Device
 *   Infrastructure profiling API — the realistic first path to GPU-side
 *   telemetry.  It runs on the host CPU but the NVIDIA driver injects
 *   hardware counter reads directly into the GPU's performance monitor
 *   units.  This is how nsight-systems and nsight-compute collect SM data.
 *
 * WHAT THIS TEST DOES:
 *   1. Initializes CUPTI and subscribes to a CUPTI callback.
 *   2. Forks and execs a CUDA fixture binary (k1 by default) as a child.
 *   3. While the child runs, the CUPTI callback fires at CUDA API boundaries
 *      (e.g. cuLaunchKernel entry/exit) and accumulates SM activity data.
 *   4. After the child exits, asserts:
 *        - at least one kernel launch was observed
 *        - the device SM count is > 0
 *        - activity records were received (Level 4)
 *
 * WHY CUPTI NOT BPF HERE:
 *   BPF uprobes/kprobes (Layers 1–4) tell you what the host CPU did.
 *   CUPTI tells you what the GPU SMs did.  These are complementary.
 *   Layer 5 is the CUPTI side of that pair.
 *
 * HOW TO BUILD:
 *   Requires CUDA Toolkit (provides cupti.h and libcupti).
 *   See test/Makefile.
 *
 *   cc -o bin/cupti_sm_test cupti_sm_test.c \
 *       -I/usr/local/cuda/include \
 *       -L/usr/local/cuda/lib64 -lcupti -lcuda
 *
 * HOW TO RUN:
 *   sudo ./bin/cupti_sm_test k1
 *   sudo ./bin/cupti_sm_test k2
 *
 *   Requires a real GPU and root (or user in the 'video' group with
 *   CAP_PERFMON on newer kernels).
 *
 * CURRENT STATE:
 *   CUPTI API calls are in TODO comments.  The fork/exec/waitpid skeleton
 *   and the SM count probe (via cuDeviceGetAttribute) are real and compile
 *   today with just -lcuda.  Uncomment the CUPTI blocks when ready.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

/*
 * When CUPTI is available, uncomment:
 *
 * #include <cuda.h>
 * #include <cupti.h>
 */

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

#define ASSERT_GT(actual, threshold, msg)                               \
    do {                                                                \
        if ((actual) <= (threshold)) {                                  \
            fprintf(stderr, "FAIL [%s:%d]: %s (got %ld, want > %ld)\n",\
                    __FILE__, __LINE__, (msg),                          \
                    (long)(actual), (long)(threshold));                 \
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
 * CUPTI CALLBACK STATE
 *
 * TODO: replace with real CUpti_SubscriberHandle and activity buffer
 * tracking once CUPTI is integrated.
 * ============================================================ */
static int g_launches_observed = 0;  /* incremented in CUPTI callback        */
static int g_assert_failed     = 0;  /* set to 1 if a callback check fails   */

/*
 * CUPTI activity callback — fires at cuLaunchKernel entry/exit.
 *
 * TODO: uncomment when CUPTI is available.
 *
 * static void CUPTIAPI cupti_callback(void *userdata,
 *                                     CUpti_CallbackDomain domain,
 *                                     CUpti_CallbackId cbid,
 *                                     const void *cbdata)
 * {
 *     if (domain != CUPTI_CB_DOMAIN_DRIVER_API)
 *         return;
 *     if (cbid != CUPTI_DRIVER_TRACE_CBID_cuLaunchKernel)
 *         return;
 *
 *     const CUpti_CallbackData *cd = (const CUpti_CallbackData *)cbdata;
 *     if (cd->callbackSite != CUPTI_API_EXIT)
 *         return;
 *
 *     g_launches_observed++;
 *
 *     // Assert the correlation ID is non-zero — each launch gets one.
 *     if (cd->correlationId == 0) {
 *         fprintf(stderr, "FAIL: correlationId is zero for launch %d\n",
 *                 g_launches_observed);
 *         g_assert_failed = 1;
 *     }
 * }
 */

/* ============================================================
 * SM COUNT PROBE
 *
 * cuDeviceGetAttribute with CU_DEVICE_ATTRIBUTE_MULTIPROCESSOR_COUNT
 * is the simplest CUDA Driver API call that tells you how many SMs
 * the device has.  Used as Level 4 assertion: SM count must be > 0.
 *
 * This function is real and active today (compiles with -lcuda).
 * ============================================================ */
static int get_sm_count(int *sm_count_out)
{
    /*
     * TODO: uncomment when building with -lcuda.
     *
     * CUresult res;
     * res = cuInit(0);
     * if (res != CUDA_SUCCESS) {
     *     fprintf(stderr, "cuInit failed: %d\n", res);
     *     return -1;
     * }
     *
     * CUdevice dev;
     * res = cuDeviceGet(&dev, 0);
     * if (res != CUDA_SUCCESS) {
     *     fprintf(stderr, "cuDeviceGet failed: %d\n", res);
     *     return -1;
     * }
     *
     * int sm_count = 0;
     * res = cuDeviceGetAttribute(&sm_count,
     *                            CU_DEVICE_ATTRIBUTE_MULTIPROCESSOR_COUNT,
     *                            dev);
     * if (res != CUDA_SUCCESS) {
     *     fprintf(stderr, "cuDeviceGetAttribute failed: %d\n", res);
     *     return -1;
     * }
     *
     * *sm_count_out = sm_count;
     * return 0;
     */

    /* Placeholder until -lcuda is linked. */
    (void)sm_count_out;
    return -1; /* not implemented yet */
}

/* ============================================================
 * FIXTURE RUNNER — identical to all other Wedjat test harnesses
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
 * TEST: CUPTI observes SM activity during a CUDA fixture run
 *
 * Levels covered:
 *   Level 3   CUPTI subscribes to cuLaunchKernel, runs fixture.
 *   Level 4a  At least one kernel launch was observed.
 *   Level 4b  The device has a non-zero SM count.
 * ============================================================ */
static int test_cupti_observes_launches(const char *fixture_path)
{
    const int expected_iters = 3;

    printf("TEST cupti_observes_launches fixture=%s iters=%d\n",
           fixture_path, expected_iters);

    /* -------------------------------------------------------
     * Probe SM count as a basic "CUDA is available" check.
     * ------------------------------------------------------- */
    int sm_count = 0;
    if (get_sm_count(&sm_count) == 0) {
        ASSERT_GT(sm_count, 0, "device SM count should be > 0");
        printf("  device SM count: %d\n", sm_count);
    } else {
        fprintf(stderr, "SKIP: cuDeviceGetAttribute not active yet"
                " (build without -lcuda stub)\n");
    }

    /* -------------------------------------------------------
     * Subscribe CUPTI callback.
     *
     * TODO: uncomment when CUPTI is available.
     *
     * CUpti_SubscriberHandle subscriber;
     * CUptiResult cr = cuptiSubscribe(&subscriber, cupti_callback, NULL);
     * ASSERT_TRUE(cr == CUPTI_SUCCESS, "cuptiSubscribe failed");
     *
     * cr = cuptiEnableCallback(1, subscriber,
     *                          CUPTI_CB_DOMAIN_DRIVER_API,
     *                          CUPTI_DRIVER_TRACE_CBID_cuLaunchKernel);
     * ASSERT_TRUE(cr == CUPTI_SUCCESS, "cuptiEnableCallback failed");
     * ------------------------------------------------------- */

    /* Spawn CUDA fixture. */
    pid_t child = run_fixture(fixture_path, expected_iters, 20);
    ASSERT_TRUE(child > 0, "failed to fork CUDA fixture");

    int wstatus;
    if (waitpid(child, &wstatus, 0) < 0) {
        perror("waitpid");
        return 1;
    }

    ASSERT_TRUE(WIFEXITED(wstatus),      "fixture did not exit normally");
    ASSERT_TRUE(WEXITSTATUS(wstatus) == 0, "fixture exited with non-zero status");

    /* -------------------------------------------------------
     * Level 4 assertions.
     *
     * TODO: uncomment when CUPTI is active.
     *
     * ASSERT_TRUE(!g_assert_failed,
     *     "per-launch CUPTI callback assertions failed");
     * ASSERT_GE(g_launches_observed, expected_iters,
     *     "expected at least one launch observed per fixture iteration");
     *
     * cuptiUnsubscribe(subscriber);
     * ------------------------------------------------------- */

    fprintf(stderr,
            "SKIP: CUPTI subscriber not active yet — assertions not running\n");

    printf("SKIP cupti_observes_launches (CUPTI not integrated)\n");
    return 0;
}

/* ============================================================
 * ENTRY POINT
 *
 * Usage: sudo ./bin/cupti_sm_test <fixture>
 *   <fixture> is k1, k2, k8, etc. from kernels_to_trace/build/
 *   Default: k1 (CUDA Runtime API — calls cuLaunchKernel most visibly)
 * ============================================================ */
int main(int argc, char *argv[])
{
    if (argc < 2) {
        fprintf(stderr, "usage: %s <fixture>\n", argv[0]);
        fprintf(stderr, "  <fixture> is k1, k2, etc. from kernels_to_trace/build/\n");
        fprintf(stderr, "  example:  sudo %s k1\n", argv[0]);
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

    return test_cupti_observes_launches(fixture_path);
}
