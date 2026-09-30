#ifndef WEDJAT_COLLECTOR_BRIDGE_H
#define WEDJAT_COLLECTOR_BRIDGE_H

#include <stdint.h>

/* ── Lifecycle ────────────────────────────────────────────────────── */

int  collector_init(void);
void collector_shutdown(void);

/* ── Device Discovery ─────────────────────────────────────────────── */

int  collector_device_count(void);

/* Fills uuid_out (must be ≥96 bytes). Returns 0 on success. */
int  collector_device_uuid(unsigned int index, char *uuid_out);

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
    unsigned long long ecc_errors;   /* uncorrected volatile */
    uint64_t valid_fields;
    int      ok;
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
};

void collector_snapshot_procs(const char *uuid, struct collector_proc_list *list);

#endif
