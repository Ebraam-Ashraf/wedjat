#ifndef WEDJAT_COLLECTOR_BRIDGE_H
#define WEDJAT_COLLECTOR_BRIDGE_H

#include <stdint.h>

/* ── Lifecycle ────────────────────────────────────────────────────── */

int collector_init(void);
void collector_shutdown(void);

/* ── Device Discovery ─────────────────────────────────────────────── */

int collector_device_count(int *nvml_error);

/* Fills uuid_out (must be ≥96 bytes). Returns 0 on success. */
int collector_device_uuid(unsigned int index, char *uuid_out, int *nvml_error);

/* ── Static Device Metadata ────────────────────────────────────────────
 * Read once at startup. Fields are stable for the lifetime of a boot, so
 * they are not part of the per-tick snapshot.
 */

#define WEDJAT_UUID_LEN 96
#define WEDJAT_NAME_LEN 128
#define WEDJAT_PCI_BUS_ID_LEN 32
#define WEDJAT_DRIVER_VERSION_LEN 96

struct collector_device_info {
    unsigned int index;
    char uuid[WEDJAT_UUID_LEN];
    char name[WEDJAT_NAME_LEN];
    char pci_bus_id[WEDJAT_PCI_BUS_ID_LEN];
    char driver_version[WEDJAT_DRIVER_VERSION_LEN];
    unsigned long long vram_total_bytes;
    int vram_valid;
    int nvml_error;
};

/* Populates info for the given device index. Returns 0 on success. */
int collector_device_info(unsigned int index, struct collector_device_info *info);

/* ── Per-Device Snapshot ──────────────────────────────────────────── */

struct collector_gpu_snap {
    unsigned int gpu_util;
    unsigned int mem_util;
    unsigned long long mem_used;
    unsigned int temp_c;
    unsigned int power_mw;
    unsigned int sm_clock_mhz;
    unsigned int mem_clock_mhz;
    unsigned int power_limit_mw;
    unsigned long long throttle_reasons;
    unsigned long long ecc_errors; /* uncorrected volatile */
    uint64_t valid_fields;
    int ok;
    int nvml_error;
};

/* Populates snap for the given UUID. */
void collector_snapshot_gpu(const char *uuid, struct collector_gpu_snap *snap);

/* ── Per-Device Process List ──────────────────────────────────────── */

struct collector_proc_entry {
    unsigned int pid;
    unsigned long long vram_bytes;
    int vram_valid;
};

struct collector_proc_list {
    unsigned int count;
    struct collector_proc_entry entries[256]; /* hard cap */
    int ok;
    int complete;
    int truncated; /* 1 if more than 256 processes were running */
    int nvml_error;
    int compute_error;
    int graphics_error;
    int mps_error;
};

void collector_snapshot_procs(const char *uuid, struct collector_proc_list *list);

struct collector_xid_event {
    uint64_t timestamp_ns;
    unsigned int device_index;
    char device_uuid[WEDJAT_UUID_LEN];
    unsigned long long nvml_event_type;
    unsigned long long nvml_event_data;
};

void *collector_xid_create(void);
int collector_xid_wait(void *set, struct collector_xid_event *event_out,
                       unsigned int timeout_ms);
void collector_xid_destroy(void *set);

#endif
