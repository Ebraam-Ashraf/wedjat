#include "bridge.h"
#include <stdio.h>
#include <string.h>

/* ── Pull in the NVML C source files via #include so they compile
 *    inside this single cgo translation unit. ──────────────────── */

#include "../nvml/poller.c"
#include "../nvml/xid.c"

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

int collector_device_count(int *nvml_error) {
    if (!nvml_error)
        return -1;
    unsigned int count = 0;
    nvmlReturn_t res = poller_device_count(&count);
    *nvml_error = res;
    if (res != NVML_SUCCESS)
        return -1;
    return (int)count;
}

int collector_device_uuid(unsigned int index, char *uuid_out, int *nvml_error) {
    if (!uuid_out || !nvml_error)
        return -1;
    struct device_metadata meta;
    poller_device_metadata(index, &meta);
    *nvml_error = meta.nvml_error;
    if (!(meta.valid_fields & DEVICE_META_UUID))
        return -1;
    snprintf(uuid_out, 96, "%s", meta.uuid);
    return 0;
}

int collector_device_info(unsigned int index, struct collector_device_info *info) {
    if (!info)
        return -1;

    struct device_metadata meta;
    struct device_snapshot ds;

    memset(info, 0, sizeof(*info));
    info->index = index;

    poller_device_metadata(index, &meta);
    info->nvml_error = meta.nvml_error;
    if (!(meta.valid_fields & DEVICE_META_UUID))
        return -1;

    snprintf(info->uuid, sizeof(info->uuid), "%s", meta.uuid);

    if (meta.valid_fields & DEVICE_META_NAME)
        snprintf(info->name, sizeof(info->name), "%s", meta.name);
    if (meta.valid_fields & DEVICE_META_PCI_BUS_ID)
        snprintf(info->pci_bus_id, sizeof(info->pci_bus_id), "%s", meta.pci_bus_id);
    if (meta.valid_fields & DEVICE_META_DRIVER_VERSION)
        snprintf(info->driver_version, sizeof(info->driver_version), "%s",
                 meta.driver_version);

    /* VRAM total comes from the snapshot path, not the metadata path. */
    poller_snapshot_device_uuid(info->uuid, &ds);
    if (info->nvml_error == NVML_SUCCESS)
        info->nvml_error = ds.nvml_error;
    if (ds.valid && (ds.valid_fields & DEVICE_VALID_MEM_TOTAL)) {
        info->vram_total_bytes = ds.mem_total;
        info->vram_valid = 1;
    }

    return 0;
}

/* ── Per-Device GPU Snapshot ───────────────────────────────────────── */

void collector_snapshot_gpu(const char *uuid, struct collector_gpu_snap *snap) {
    struct device_snapshot ds;

    memset(snap, 0, sizeof(*snap));
    poller_snapshot_device_uuid(uuid, &ds);
    snap->nvml_error = ds.nvml_error;
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
        list->nvml_error = ps.nvml_error;
        list->compute_error = ps.compute_error;
        list->graphics_error = ps.graphics_error;
        list->mps_error = ps.mps_error;
        /* poller_snapshot_processes_uuid already freed ps.entries when
         * ps.valid is false, so calling poller_process_snapshot_destroy
         * here would be a double-free. Only destroy when valid == 1. */
        return;
    }
    list->ok = 1;
    list->complete = ps.complete;
    list->nvml_error = ps.nvml_error;
    list->compute_error = ps.compute_error;
    list->graphics_error = ps.graphics_error;
    list->mps_error = ps.mps_error;

    unsigned int cap = ps.count;
    if (cap > 256) {
        cap = 256;
        list->truncated = 1;
        list->complete = 0;
    }
    list->count = cap;

    for (unsigned int i = 0; i < cap; i++) {
        list->entries[i].pid        = ps.entries[i].pid;
        list->entries[i].vram_bytes = ps.entries[i].used_gpu_memory;
        list->entries[i].vram_valid = ps.entries[i].memory_valid;
    }
    poller_process_snapshot_destroy(&ps);
}
