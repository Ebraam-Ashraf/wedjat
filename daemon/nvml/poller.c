/*
 * daemon/nvml/poller.c
 *
 * Layer 4: NVML Poller — periodic GPU device and process stats.
 *
 * WHAT THIS FILE IS:
 *   Plain user-space C that calls libnvidia-ml.so to read physical GPU state.
 *   No eBPF, no BPF helpers, no kernel involvement.  Links against
 *   -lnvidia-ml at build time; the library is provided by the NVIDIA driver.
 *
 *   This is the "how busy is the GPU" side of Wedjat.
 *   The eBPF layers (host_ctx, cuda_actions, driver_kprobes) answer
 *   "who called CUDA and what did they ask for."
 *   This file answers "what did the physical GPU actually do."
 *
 * LAYER 4 ANSWERS:
 *   - How busy is the physical GPU?              nvmlDeviceGetUtilizationRates
 *   - How much VRAM is used/free/total?          nvmlDeviceGetMemoryInfo
 *   - Temperature, power draw, clock speeds?     nvmlDeviceGetTemperature etc.
 *   - Which PIDs are active compute processes?   nvmlDeviceGetComputeRunningProcesses_v3
 *
 * HOW THIS CONNECTS TO LAYER 1/2:
 *   eBPF gives pid/tid/cgroup/API call events.
 *   NVML gives physical device totals and per-process VRAM usage.
 *   The daemon joins them on: device UUID + pid + 1-second timestamp bucket.
 *
 * TESTABLE INTERFACE RULE:
 *   Every function in this file returns a struct or fills one passed by
 *   pointer.  Nothing prints directly to stdout.  The test harness
 *   (test/poller_test.c) calls these functions and asserts on the values.
 *
 * CURRENT STATE:
 *   Function signatures and structs are defined.  Bodies contain the real
 *   NVML call sequence in TODO comments.  Once nvml.h is available, remove
 *   the TODO markers and uncomment the calls.
 *
 * BUILD:
 *   cc -o poller.o -c poller.c -I/usr/local/cuda/include
 *   link with: -lnvidia-ml
 */

#include <stddef.h>
#include <string.h>

/*
 * When building for real, uncomment:
 *
 * #include <nvml.h>
 */

/* ============================================================
 * STRUCTS
 *
 * These are the data types the test harness asserts against.
 * Must be stable — do not reorder fields without updating the tests.
 * ============================================================ */

/*
 * wedjat_device_snapshot — one polling cycle for one GPU.
 *
 * Filled by poller_snapshot_device().
 * All physical-device fields that do not require a running process.
 */
struct wedjat_device_snapshot {
    char   uuid[96];         /* nvmlDeviceGetUUID — stable cross-reboot id    */
    unsigned int index;      /* device ordinal (0, 1, ...)                    */

    unsigned int gpu_util;   /* percent, 0–100                                */
    unsigned int mem_util;   /* percent, 0–100                                */

    unsigned long long mem_used;   /* bytes, from nvmlDeviceGetMemoryInfo     */
    unsigned long long mem_free;   /* bytes                                   */
    unsigned long long mem_total;  /* bytes                                   */

    unsigned int temp_c;     /* Celsius, nvmlDeviceGetTemperature             */
    unsigned int power_mw;   /* milliwatts, nvmlDeviceGetPowerUsage           */

    unsigned int sm_clock_mhz;   /* nvmlDeviceGetClockInfo NVML_CLOCK_SM     */
    unsigned int mem_clock_mhz;  /* nvmlDeviceGetClockInfo NVML_CLOCK_MEM    */

    int          valid;      /* 1 if all fields were read without error       */
    int          nvml_error; /* last nvmlReturn_t if valid == 0               */
};

/*
 * wedjat_process_entry — one active compute process as NVML sees it.
 *
 * Filled by poller_snapshot_processes() in the entries[] array.
 */
struct wedjat_process_entry {
    unsigned int           pid;
    unsigned long long     used_gpu_memory; /* bytes reported by NVML        */
};

/*
 * wedjat_process_snapshot — result of one nvmlDeviceGetComputeRunningProcesses call.
 */
#define WEDJAT_MAX_PROCESSES 64

struct wedjat_process_snapshot {
    unsigned int              count;                          /* number of active processes   */
    struct wedjat_process_entry entries[WEDJAT_MAX_PROCESSES];
    int                        valid;
    int                        nvml_error;
};

/* ============================================================
 * FORWARD DECLARATIONS
 *
 * nvmlDevice_t is an opaque pointer type from nvml.h.
 * We forward-declare it here as void* so this file compiles without
 * nvml.h.  Replace with the real type when building for real.
 * ============================================================ */
typedef void *nvml_device_handle_t; /* placeholder — real type is nvmlDevice_t */

/* ============================================================
 * poller_init
 *
 * Initializes the NVML library.  Must be called once before any other
 * function in this file.
 *
 * Returns 0 on success, -1 on failure.
 * ============================================================ */
int poller_init(void)
{
    /*
     * TODO: uncomment when nvml.h is available.
     *
     * nvmlReturn_t r = nvmlInit_v2();
     * if (r != NVML_SUCCESS) {
     *     fprintf(stderr, "nvmlInit_v2 failed: %s\n", nvmlErrorString(r));
     *     return -1;
     * }
     * return 0;
     */
    return -1; /* not implemented yet */
}

/* ============================================================
 * poller_shutdown
 *
 * Shuts down the NVML library.  Call once at daemon exit.
 * ============================================================ */
void poller_shutdown(void)
{
    /*
     * TODO: uncomment when nvml.h is available.
     *
     * nvmlShutdown();
     */
}

/* ============================================================
 * poller_device_count
 *
 * Returns the number of NVIDIA GPUs visible to NVML.
 * Returns -1 on error.
 * ============================================================ */
int poller_device_count(void)
{
    /*
     * TODO: uncomment when nvml.h is available.
     *
     * unsigned int count = 0;
     * nvmlReturn_t r = nvmlDeviceGetCount_v2(&count);
     * if (r != NVML_SUCCESS)
     *     return -1;
     * return (int)count;
     */
    return -1;
}

/* ============================================================
 * poller_snapshot_device
 *
 * Reads all physical-device stats for device at ordinal `index` into `out`.
 *
 * Sets out->valid = 1 on full success.
 * Sets out->valid = 0 and out->nvml_error on the first NVML failure.
 * Partial reads (some fields valid, others not) are not attempted —
 * the whole snapshot is marked invalid on any error.
 * ============================================================ */
void poller_snapshot_device(unsigned int index, struct wedjat_device_snapshot *out)
{
    memset(out, 0, sizeof(*out));
    out->index = index;

    /*
     * TODO: uncomment when nvml.h is available.
     *
     * nvmlDevice_t dev;
     * nvmlReturn_t r;
     *
     * // Get device handle.
     * r = nvmlDeviceGetHandleByIndex_v2(index, &dev);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     *
     * // UUID — stable identifier across reboots.
     * r = nvmlDeviceGetUUID(dev, out->uuid, sizeof(out->uuid));
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     *
     * // Utilization rates (GPU + memory controller).
     * nvmlUtilization_t util;
     * r = nvmlDeviceGetUtilizationRates(dev, &util);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     * out->gpu_util = util.gpu;
     * out->mem_util = util.memory;
     *
     * // Memory info.
     * nvmlMemory_t mem;
     * r = nvmlDeviceGetMemoryInfo(dev, &mem);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     * out->mem_used  = mem.used;
     * out->mem_free  = mem.free;
     * out->mem_total = mem.total;
     *
     * // Temperature.
     * r = nvmlDeviceGetTemperature(dev, NVML_TEMPERATURE_GPU, &out->temp_c);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     *
     * // Power draw.
     * r = nvmlDeviceGetPowerUsage(dev, &out->power_mw);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     *
     * // Clock speeds.
     * r = nvmlDeviceGetClockInfo(dev, NVML_CLOCK_SM,  &out->sm_clock_mhz);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     * r = nvmlDeviceGetClockInfo(dev, NVML_CLOCK_MEM, &out->mem_clock_mhz);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     *
     * out->valid = 1;
     */
}

/* ============================================================
 * poller_snapshot_processes
 *
 * Reads the list of active compute processes for device at ordinal `index`
 * into `out`.
 *
 * Uses nvmlDeviceGetComputeRunningProcesses_v3 (preferred) which also
 * reports per-process VRAM usage.
 *
 * Sets out->valid = 1 on success (even if count == 0 — no active processes
 * is a valid state, not an error).
 * Sets out->valid = 0 and out->nvml_error on NVML failure.
 * ============================================================ */
void poller_snapshot_processes(unsigned int index, struct wedjat_process_snapshot *out)
{
    memset(out, 0, sizeof(*out));

    /*
     * TODO: uncomment when nvml.h is available.
     *
     * nvmlDevice_t dev;
     * nvmlReturn_t r;
     *
     * r = nvmlDeviceGetHandleByIndex_v2(index, &dev);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     *
     * // nvmlProcessInfo_t is the struct NVML fills.
     * // We map it to wedjat_process_entry after the call.
     * nvmlProcessInfo_t procs[WEDJAT_MAX_PROCESSES];
     * unsigned int count = WEDJAT_MAX_PROCESSES;
     *
     * r = nvmlDeviceGetComputeRunningProcesses_v3(dev, &count, procs);
     * if (r != NVML_SUCCESS) { out->nvml_error = (int)r; return; }
     *
     * out->count = count < WEDJAT_MAX_PROCESSES ? count : WEDJAT_MAX_PROCESSES;
     * for (unsigned int i = 0; i < out->count; i++) {
     *     out->entries[i].pid             = procs[i].pid;
     *     out->entries[i].used_gpu_memory = procs[i].usedGpuMemory;
     * }
     *
     * out->valid = 1;
     */
}

/* ============================================================
 * poller_find_pid
 *
 * Searches a wedjat_process_snapshot for a specific pid.
 * Returns a pointer to the matching entry, or NULL if not found.
 *
 * Used by the test harness to assert that a known-pid fixture appears
 * in the process list with a plausible memory footprint.
 * ============================================================ */
const struct wedjat_process_entry *poller_find_pid(
        const struct wedjat_process_snapshot *snap,
        unsigned int pid)
{
    for (unsigned int i = 0; i < snap->count; i++) {
        if (snap->entries[i].pid == pid)
            return &snap->entries[i];
    }
    return NULL;
}
