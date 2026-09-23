/*
 * daemon/nvml/xid.c
 *
 * Layer 4: NVML Xid / hardware fault event watcher.
 *
 * WHAT THIS FILE IS:
 *   User-space C that subscribes to NVIDIA hardware fault events via NVML.
 *   No eBPF.  Links against -lnvidia-ml.
 *
 *   Xid errors are driver/hardware fault signals — GPU hangs, ECC faults,
 *   MMU faults, page retirements, etc.  They do NOT come from the CUDA API
 *   call path (that is Layers 1–3).  They come from a separate NVML event
 *   channel and also appear in dmesg as "NVRM: Xid (PCI:...)".
 *
 * LAYER 4 FAULT ANSWERS:
 *   - Did the GPU hang?                          Xid 79 (GPU hang)
 *   - Did a CUDA kernel cause an illegal fault?  Xid 13 / 31 / 43
 *   - Is ECC memory correctable/uncorrectable?   Xid 48 / 74
 *   - Are pages being retired?                   Xid 63 / 94
 *
 * TESTABILITY DESIGN:
 *   Hardware faults cannot be provoked on demand in automated tests.
 *   This file is therefore designed in two separable pieces:
 *
 *   1. Plumbing (testable):
 *      xid_event_set_create(), xid_event_set_register(), xid_event_set_wait()
 *      These can be called in a test: create the set, register for events,
 *      wait with a short timeout.  A clean NVML_ERROR_TIMEOUT is the pass
 *      condition — the wiring works even though no fault fired.
 *
 *   2. Fault handling (manual only):
 *      xid_handle_event() — processes a real captured event.
 *      Cannot be automatically exercised without a real hardware fault.
 *      See test/xid_test.c --manual mode.
 *
 * CURRENT STATE:
 *   Function signatures and structs are defined.  Bodies have real NVML
 *   call sequences in TODO comments.  Uncomment when nvml.h is available.
 *
 * BUILD:
 *   cc -o xid.o -c xid.c -I/usr/local/cuda/include
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
 * ============================================================ */

/*
 * wedjat_xid_event — one captured hardware fault event.
 *
 * Filled by xid_handle_event() from a real nvmlEventData_t.
 * Stored as a slow-path record (rare, not aggregated).
 *
 * NOTE on pid mapping:
 *   Some Xid codes are device-wide — no single process caused them.
 *   We store the associated_pid only if NVML reports one; otherwise 0.
 *   "associated active PIDs at fault time" would be a separate NVML query
 *   via nvmlDeviceGetComputeRunningProcesses — not done inline here because
 *   it adds latency inside what might be a time-sensitive event handler.
 */
struct wedjat_xid_event {
    unsigned long long timestamp_ns;  /* bpf_ktime_get_ns() equivalent in userspace:
                                       * clock_gettime(CLOCK_BOOTTIME, ...) */
    unsigned int       device_index;
    char               device_uuid[96];

    unsigned long long nvml_event_type;  /* nvmlEventData_t.eventType          */
    unsigned long long nvml_event_data;  /* nvmlEventData_t.eventData (Xid num) */

    unsigned int       associated_pid;   /* 0 if device-wide or unknown        */
};

/*
 * xid_event_set — opaque handle wrapping the NVML event set.
 *
 * Callers use xid_event_set_create() to get one and
 * xid_event_set_destroy() to release it.
 * Internals are hidden so the test harness does not need nvml.h.
 */
struct xid_event_set {
    int valid;          /* 1 if create+register succeeded                     */
    int nvml_error;     /* last nvmlReturn_t if valid == 0                    */

    /*
     * TODO: add when nvml.h is available:
     *
     * nvmlEventSet_t  nvml_set;
     * nvmlDevice_t    device;
     * unsigned int    device_index;
     */
};

/* ============================================================
 * EVENT MASK
 *
 * Which NVML events to subscribe to.
 * Start with Xid critical errors — the most important fault signal.
 * Add ECC / clock / power events later as needed.
 * ============================================================ */
#define WEDJAT_XID_EVENT_MASK  (1ULL << 0)  /* placeholder — real value is
                                               nvmlEventTypeXidCriticalError */

/* ============================================================
 * xid_event_set_create
 *
 * Allocates and returns an xid_event_set for device at ordinal `index`.
 * Registers for WEDJAT_XID_EVENT_MASK events.
 *
 * Returns a pointer to a static set (single-device daemon use).
 * Returns NULL on failure.
 *
 * NVML_ERROR_NOT_SUPPORTED is treated as a soft failure — some GPUs or
 * driver configs do not support event registration.  The caller should
 * log a warning and continue without fault monitoring rather than abort.
 * ============================================================ */
struct xid_event_set *xid_event_set_create(unsigned int device_index)
{
    static struct xid_event_set s_set;
    memset(&s_set, 0, sizeof(s_set));

    /*
     * TODO: uncomment when nvml.h is available.
     *
     * nvmlReturn_t r;
     *
     * r = nvmlDeviceGetHandleByIndex_v2(device_index, &s_set.device);
     * if (r != NVML_SUCCESS) { s_set.nvml_error = (int)r; return &s_set; }
     *
     * s_set.device_index = device_index;
     *
     * r = nvmlEventSetCreate(&s_set.nvml_set);
     * if (r != NVML_SUCCESS) { s_set.nvml_error = (int)r; return &s_set; }
     *
     * unsigned long long mask = nvmlEventTypeXidCriticalError;
     * r = nvmlDeviceRegisterEvents(s_set.device, mask, s_set.nvml_set);
     * if (r == NVML_ERROR_NOT_SUPPORTED) {
     *     // Soft failure — GPU or driver does not support event registration.
     *     // Caller should log and skip, not abort.
     *     s_set.nvml_error = (int)r;
     *     return &s_set;
     * }
     * if (r != NVML_SUCCESS) { s_set.nvml_error = (int)r; return &s_set; }
     *
     * s_set.valid = 1;
     */

    return &s_set;
}

/* ============================================================
 * xid_event_set_wait
 *
 * Waits up to `timeout_ms` milliseconds for a fault event.
 * Fills `event_out` if one arrives.
 *
 * Return values:
 *   0                    event received, event_out is valid
 *   XID_WAIT_TIMEOUT     timeout expired, no event (normal in automated tests)
 *   XID_WAIT_ERROR       NVML returned an unexpected error
 *   XID_WAIT_NOT_SUPPORTED  registration was not supported on this device
 * ============================================================ */
#define XID_WAIT_TIMEOUT        1
#define XID_WAIT_ERROR          2
#define XID_WAIT_NOT_SUPPORTED  3

int xid_event_set_wait(struct xid_event_set *set,
                       struct wedjat_xid_event *event_out,
                       unsigned int timeout_ms)
{
    if (!set->valid) {
        /* Check if the failure was NOT_SUPPORTED — soft, not hard. */
        /* TODO: compare set->nvml_error to NVML_ERROR_NOT_SUPPORTED */
        return XID_WAIT_NOT_SUPPORTED;
    }

    /*
     * TODO: uncomment when nvml.h is available.
     *
     * nvmlEventData_t data;
     * nvmlReturn_t r = nvmlEventSetWait(set->nvml_set, &data, timeout_ms);
     *
     * if (r == NVML_ERROR_TIMEOUT)
     *     return XID_WAIT_TIMEOUT;
     *
     * if (r != NVML_SUCCESS)
     *     return XID_WAIT_ERROR;
     *
     * // Populate event_out.
     * struct timespec ts;
     * clock_gettime(CLOCK_BOOTTIME, &ts);
     * event_out->timestamp_ns   = (unsigned long long)ts.tv_sec * 1000000000ULL
     *                           + ts.tv_nsec;
     * event_out->device_index   = set->device_index;
     * event_out->nvml_event_type = data.eventType;
     * event_out->nvml_event_data = data.eventData;
     *
     * // UUID for stable cross-reboot identity.
     * nvmlDeviceGetUUID(set->device,
     *                   event_out->device_uuid,
     *                   sizeof(event_out->device_uuid));
     *
     * // associated_pid: only available if NVML reports it through eventData.
     * // Some Xid codes carry the faulting context / PID; many do not.
     * event_out->associated_pid = 0;  // TODO: parse from data if available
     *
     * return 0;
     */

    (void)event_out;
    (void)timeout_ms;
    return XID_WAIT_ERROR; /* not implemented yet */
}

/* ============================================================
 * xid_handle_event
 *
 * Processes a captured wedjat_xid_event — logs it, correlates with
 * running processes if possible, and passes it to the store layer.
 *
 * Separated from xid_event_set_wait() so the event handling logic can
 * be tested independently of the NVML wait path.
 *
 * Returns 0 on success, -1 if logging/store fails.
 * ============================================================ */
int xid_handle_event(const struct wedjat_xid_event *event)
{
    /*
     * TODO: implement when store layer exists.
     *
     * 1. Log to structured store (sqlite / JSONL).
     * 2. Attempt to correlate with nvmlDeviceGetComputeRunningProcesses
     *    at this instant — store as "associated_pids[]", not a single owner.
     * 3. Emit a daemon-level warning for critical Xid codes (e.g. 79 = hang).
     */
    (void)event;
    return -1; /* not implemented yet */
}

/* ============================================================
 * xid_event_set_destroy
 *
 * Frees the NVML event set.  Call at daemon shutdown.
 * ============================================================ */
void xid_event_set_destroy(struct xid_event_set *set)
{
    if (!set || !set->valid)
        return;

    /*
     * TODO: uncomment when nvml.h is available.
     *
     * nvmlEventSetFree(set->nvml_set);
     */
}
