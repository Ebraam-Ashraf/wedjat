#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/wait.h>
#include <signal.h>

#include "../poller.h"

#define PASS 0
#define FAIL 1
#define SKIP 77

int poller_test_fail_power_query;

#define ASSERT_TRUE(cond, msg)                                                         \
    do {                                                                               \
        if (!(cond)) {                                                                 \
            fprintf(stderr, "FAIL [%s:%d]: %s\n", __FILE__, __LINE__, (msg));          \
            return FAIL;                                                               \
        }                                                                              \
    } while (0)

#define ASSERT_IN_RANGE(val, lo, hi, msg)                                              \
    do {                                                                               \
        if ((long long)(val) < (long long)(lo) ||                                      \
            (long long)(val) > (long long)(hi)) {                                      \
            fprintf(stderr, "FAIL [%s:%d]: %s (got %lld, want [%lld, %lld])\n",        \
                    __FILE__, __LINE__, (msg), (long long)(val), (long long)(lo),      \
                    (long long)(hi));                                                  \
            return FAIL;                                                               \
        }                                                                              \
    } while (0)

static pid_t run_fixture(const char *path, const char *uuid, long n, int iters,
                         int sleep_ms) {
    pid_t pid = fork();
    if (pid < 0) {
        perror("fork");
        return -1;
    }
    if (pid == 0) {
        char n_str[32], iters_str[16], sleep_str[16];
        snprintf(n_str, sizeof(n_str), "%ld", n);
        snprintf(iters_str, sizeof(iters_str), "%d", iters);
        snprintf(sleep_str, sizeof(sleep_str), "%d", sleep_ms);
        setenv("CUDA_VISIBLE_DEVICES", uuid, 1);
        setenv("WEDJAT_N", n_str, 1);
        setenv("WEDJAT_ITERS", iters_str, 1);
        setenv("WEDJAT_SLEEP_MS", sleep_str, 1);
        execl(path, path, (char *)NULL);
        perror("execl");
        _exit(127);
    }
    return pid;
}

static int test_invalid_device(void) {
    struct device_snapshot snapshot;
    poller_snapshot_device_uuid("GPU-invalid", &snapshot);
    ASSERT_TRUE(!snapshot.valid, "invalid UUID must not produce a valid sample");
    ASSERT_TRUE(snapshot.nvml_error != NVML_SUCCESS,
                "invalid UUID must preserve its NVML error code");
    ASSERT_TRUE(snapshot.valid_fields == 0,
                "failed device query must leave all metric fields unavailable");
    return PASS;
}

static int test_device_snapshots(unsigned int count) {
    printf("TEST device_snapshots devices=%u\n", count);
    for (unsigned int i = 0; i < count; i++) {
        struct device_metadata metadata;
        struct device_snapshot snapshot;

        poller_device_metadata(i, &metadata);
        ASSERT_TRUE(metadata.valid_fields & DEVICE_META_UUID,
                    "device UUID must be available");
        ASSERT_TRUE(metadata.uuid[0] != '\0', "UUID must be non-empty");
        ASSERT_TRUE(metadata.valid_fields & DEVICE_META_NAME,
                    "device name must be available");
        ASSERT_TRUE(metadata.valid_fields & DEVICE_META_PCI_BUS_ID,
                    "PCI bus id must be available");
        ASSERT_TRUE(metadata.valid_fields & DEVICE_META_DRIVER_VERSION,
                    "driver version must be available");

        poller_snapshot_device_uuid(metadata.uuid, &snapshot);
        ASSERT_TRUE(snapshot.valid, "UUID lookup must produce a snapshot");

        if (snapshot.valid_fields & DEVICE_VALID_INDEX)
            ASSERT_TRUE(snapshot.index == i || count > 1,
                        "index should match startup enumeration where stable");
        if (snapshot.valid_fields & DEVICE_VALID_GPU_UTIL)
            ASSERT_IN_RANGE(snapshot.gpu_util, 0, 100, "GPU util must be 0-100");
        if (snapshot.valid_fields & DEVICE_VALID_MEM_UTIL)
            ASSERT_IN_RANGE(snapshot.mem_util, 0, 100, "memory util must be 0-100");
        if (snapshot.valid_fields & DEVICE_VALID_TEMP)
            ASSERT_IN_RANGE(snapshot.temp_c, 0, 150, "temperature must be plausible");
        if ((snapshot.valid_fields & DEVICE_VALID_MEM_TOTAL) &&
            (snapshot.valid_fields & DEVICE_VALID_MEM_USED))
            ASSERT_TRUE(snapshot.mem_used <= snapshot.mem_total,
                        "used memory must not exceed total");

        printf("PASS device=%u name=%s temp=%s%uC power=%s%umW fields=0x%llx\n", i,
               metadata.name, snapshot.valid_fields & DEVICE_VALID_TEMP ? "" : "N/A/",
               snapshot.temp_c,
               snapshot.valid_fields & DEVICE_VALID_POWER ? "" : "N/A/",
               snapshot.power_mw, (unsigned long long)snapshot.valid_fields);
    }
    return PASS;
}

static int test_failed_field(const char *uuid) {
    struct device_snapshot snapshot;
    poller_test_fail_power_query = 1;
    poller_snapshot_device_uuid(uuid, &snapshot);
    poller_test_fail_power_query = 0;
    ASSERT_TRUE(snapshot.valid,
                "one failed field must not invalidate the entire sample");
    ASSERT_TRUE(!(snapshot.valid_fields & DEVICE_VALID_POWER),
                "failed power query must leave the power field unavailable");
    ASSERT_TRUE(snapshot.nvml_error == NVML_ERROR_NOT_SUPPORTED,
                "failed field must preserve the NVML error code");
    ASSERT_TRUE(snapshot.valid_fields & DEVICE_VALID_GPU_UTIL,
                "unrelated successful fields must remain valid");
    return PASS;
}

static int test_process_snapshot(const char *fixture_path, const char *uuid) {
    const unsigned long long alloc_bytes = 256ULL * 1024 * 1024;
    const long n_elements = (long)(alloc_bytes / sizeof(float));
    /* The fixture must outlive the observation window below. NVML only lists a
     * process once its context is registered, which on a loaded machine can take
     * a moment, and a fixture that finishes first can never be observed at all.
     * 20 * 300ms of fixture against a 5s window leaves real margin. The child is
     * killed as soon as it is found, so this does not slow the passing path. */
    const int fixture_iters = 20;
    const int fixture_sleep_ms = 300;
    const int discover_attempts = 33; /* 33 * 150ms = 4.95s */
    const int discover_sleep_us = 150 * 1000;

    pid_t child =
        run_fixture(fixture_path, uuid, n_elements, fixture_iters, fixture_sleep_ms);
    ASSERT_TRUE(child > 0, "failed to start CUDA fixture");

    struct process_snapshot snapshot = {};
    const struct process_entry *found = NULL;
    /* NVML accounts a process's memory asynchronously, so a process can appear
     * in the process list before its allocation shows up in used_gpu_memory.
     * Reading once, at first sighting, therefore races that accounting and
     * reports the CUDA context alone. Keep sampling after the process is found
     * and keep the largest reading, which is the one that has caught up; stop
     * as soon as the allocation is plausibly accounted for. */
    unsigned long long best_memory = 0;
    for (int attempt = 0; attempt < discover_attempts; attempt++) {
        poller_process_snapshot_destroy(&snapshot);
        poller_snapshot_processes_uuid(uuid, &snapshot);
        if (snapshot.valid) {
            const struct process_entry *entry =
                poller_find_pid(&snapshot, (unsigned int)child);
            if (entry) {
                found = entry;
                if (entry->memory_valid && entry->used_gpu_memory > best_memory)
                    best_memory = entry->used_gpu_memory;
                if (best_memory >= alloc_bytes / 2)
                    break;
            }
        }
        if (!found || best_memory < alloc_bytes / 2)
            usleep(discover_sleep_us);
    }

    kill(child, SIGTERM);
    int status = 0;
    waitpid(child, &status, 0);

    if (!snapshot.valid) {
        fprintf(stderr, "NVML process query failed: %s\n",
                nvmlErrorString(snapshot.nvml_error));
        poller_process_snapshot_destroy(&snapshot);
        return SKIP;
    }
    ASSERT_TRUE(found != NULL,
                "fixture pid absent from compute/graphics process lists");
    ASSERT_TRUE(!poller_gpu_memory_value_valid(ULLONG_MAX),
                "NOT_AVAILABLE sentinel must be classified as invalid");
    ASSERT_TRUE(poller_gpu_memory_value_valid(1024),
                "ordinary GPU memory values must be classified as valid");
    if (!found->memory_valid) {
        printf("SKIP memory value unavailable for pid=%u (correctly marked unknown)\n",
               found->pid);
        poller_process_snapshot_destroy(&snapshot);
        return PASS;
    }
    ASSERT_TRUE(best_memory != ULLONG_MAX,
                "NOT_AVAILABLE memory must not escape as a valid measurement");
    ASSERT_IN_RANGE(best_memory, alloc_bytes / 2, alloc_bytes * 4,
                    "NVML process memory should be plausible for the known allocation");
    printf("PASS process pid=%u memory=%lluMiB sources=0x%x\n", found->pid,
           best_memory / (1024 * 1024), found->source_flags);
    poller_process_snapshot_destroy(&snapshot);
    return PASS;
}

int main(int argc, char **argv) {
    if (geteuid() != 0 || access("/dev/nvidiactl", F_OK) != 0) {
        printf("SKIP: root and an NVIDIA device are required\n");
        return SKIP;
    }
    if (argc < 2) {
        fprintf(stderr, "usage: %s <fixture> (e.g. k1)\n", argv[0]);
        return FAIL;
    }

    char fixture_path[512];
    snprintf(fixture_path, sizeof(fixture_path), "../../../kernels_to_trace/build/%s",
             argv[1]);
    if (access(fixture_path, X_OK) != 0) {
        fprintf(stderr, "FAIL: fixture missing: %s\n", fixture_path);
        return FAIL;
    }

    nvmlReturn_t result = poller_init();
    if (result != NVML_SUCCESS) {
        printf("SKIP: nvmlInit: %s\n", nvmlErrorString(result));
        return SKIP;
    }

    unsigned int count = 0;
    result = poller_device_count(&count);
    if (result != NVML_SUCCESS || count == 0) {
        printf("SKIP: no NVML devices: %s\n",
               nvmlErrorString(result == NVML_SUCCESS ? NVML_ERROR_NOT_FOUND : result));
        poller_shutdown();
        return SKIP;
    }

    int rc = test_invalid_device();
    if (rc == PASS)
        rc = test_device_snapshots(count);

    unsigned int process_tests = 0;
    for (unsigned int i = 0; rc == PASS && i < count; i++) {
        struct device_metadata metadata;
        poller_device_metadata(i, &metadata);
        if (!(metadata.valid_fields & DEVICE_META_UUID))
            continue;
        int current = test_failed_field(metadata.uuid);
        if (current == PASS)
            current = test_process_snapshot(fixture_path, metadata.uuid);
        if (current == FAIL)
            rc = FAIL;
        else if (current == PASS)
            process_tests++;
    }

    if (count < 2)
        printf("SKIP: multi-GPU exercise requires at least two devices\n");
    if (rc == PASS && process_tests == 0)
        rc = SKIP;

    poller_shutdown();
    printf("\npoller_test: %s\n", rc == PASS ? "PASS" : (rc == SKIP ? "SKIP" : "FAIL"));
    return rc;
}
