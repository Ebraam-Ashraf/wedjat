#include "bridge.h"
#include <stdio.h>
#include <string.h>

/* ── Pull in the NVML C source files via #include so they compile
 *    inside this single cgo translation unit. ──────────────────── */

#include "../nvml/poller.c"

/* xid.c has a static keep_first_error with a different signature;
   rename it to avoid the collision with poller.c's version. */
#define keep_first_error xid_keep_first_error
#include "../nvml/xid.c"
#undef keep_first_error

/* ── Lifecycle ────────────────────────────────────────────────────── */

int collector_init(void) {
    nvmlReturn_t res = poller_init();
    if (res != NVML_SUCCESS) {
        fprintf(stderr, "collector: nvmlInit failed (%d)\n", (int)res);
        return -1;
    }
    return 0;
}

void collector_shutdown(void) {
    poller_shutdown();
}

/* ── Device Discovery ─────────────────────────────────────────────── */

int collector_device_count(void) {
    unsigned int count = 0;
    nvmlReturn_t res = poller_device_count(&count);
    if (res != NVML_SUCCESS)
        return -1;
    return (int)count;
}

int collector_device_uuid(unsigned int index, char *uuid_out) {
    struct device_metadata meta;
    poller_device_metadata(index, &meta);
    if (!(meta.valid_fields & DEVICE_META_UUID))
        return -1;
    snprintf(uuid_out, 96, "%s", meta.uuid);
    return 0;
}

/* ── Per-Device GPU Snapshot ───────────────────────────────────────── */

void collector_snapshot_gpu(const char *uuid, struct collector_gpu_snap *snap) {
    struct device_snapshot ds;

    memset(snap, 0, sizeof(*snap));
    poller_snapshot_device_uuid(uuid, &ds);
    if (!ds.valid) {
        snap->ok = 0;
        return;
    }
    snap->ok = 1;
    snap->valid_fields = ds.valid_fields;

    if (ds.valid_fields & DEVICE_VALID_GPU_UTIL)
        snap->gpu_util  = ds.gpu_util;
    if (ds.valid_fields & DEVICE_VALID_MEM_UTIL)
        snap->mem_util  = ds.mem_util;
    if (ds.valid_fields & DEVICE_VALID_MEM_USED)
        snap->mem_used  = ds.mem_used;
    if (ds.valid_fields & DEVICE_VALID_TEMP)
        snap->temp_c    = ds.temp_c;
    if (ds.valid_fields & DEVICE_VALID_POWER)
        snap->power_mw  = ds.power_mw;
    if (ds.valid_fields & DEVICE_VALID_SM_CLOCK)
        snap->sm_clock_mhz = ds.sm_clock_mhz;
    if (ds.valid_fields & DEVICE_VALID_MEM_CLOCK)
        snap->mem_clock_mhz = ds.mem_clock_mhz;
    if (ds.valid_fields & DEVICE_VALID_POWER_LIMIT)
        snap->power_limit_mw = ds.power_limit_mw;
    if (ds.valid_fields & DEVICE_VALID_THROTTLE_REASONS)
        snap->throttle_reasons = ds.throttle_reasons;
    if (ds.valid_fields & DEVICE_VALID_ECC_UNCORRECTED_VOLATILE)
        snap->ecc_errors = ds.ecc_uncorrected_volatile;
}

/* ── Per-Device Process Snapshot ───────────────────────────────────── */

void collector_snapshot_procs(const char *uuid, struct collector_proc_list *list) {
    struct process_snapshot ps;

    memset(list, 0, sizeof(*list));
    poller_snapshot_processes_uuid(uuid, &ps);
    if (!ps.valid) {
        list->ok = 0;
        return;
    }
    list->ok = 1;

    unsigned int cap = ps.count;
    if (cap > 256)
        cap = 256;
    list->count = cap;

    for (unsigned int i = 0; i < cap; i++) {
        list->entries[i].pid        = ps.entries[i].pid;
        list->entries[i].vram_bytes = ps.entries[i].used_gpu_memory;
        list->entries[i].vram_valid = ps.entries[i].memory_valid;
    }
    poller_process_snapshot_destroy(&ps);
}
