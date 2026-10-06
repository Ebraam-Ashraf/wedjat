#ifndef WEDJAT_NVML_POLLER_H
#define WEDJAT_NVML_POLLER_H

#include <nvml.h>
#include <stdint.h>

enum device_metadata_field {
    DEVICE_META_UUID = 1ULL << 0,
    DEVICE_META_NAME = 1ULL << 1,
    DEVICE_META_PCI_BUS_ID = 1ULL << 2,
    DEVICE_META_DRIVER_VERSION = 1ULL << 3
};

enum device_snapshot_field {
    DEVICE_VALID_INDEX = 1ULL << 0,
    DEVICE_VALID_GPU_UTIL = 1ULL << 1,
    DEVICE_VALID_MEM_UTIL = 1ULL << 2,
    DEVICE_VALID_MEM_USED = 1ULL << 3,
    DEVICE_VALID_MEM_FREE = 1ULL << 4,
    DEVICE_VALID_MEM_TOTAL = 1ULL << 5,
    DEVICE_VALID_TEMP = 1ULL << 6,
    DEVICE_VALID_POWER = 1ULL << 7,
    DEVICE_VALID_SM_CLOCK = 1ULL << 8,
    DEVICE_VALID_MEM_CLOCK = 1ULL << 9,
    DEVICE_VALID_THROTTLE_REASONS = 1ULL << 10,
    DEVICE_VALID_POWER_LIMIT = 1ULL << 11,
    DEVICE_VALID_ECC_CORRECTED_VOLATILE = 1ULL << 12,
    DEVICE_VALID_ECC_UNCORRECTED_VOLATILE = 1ULL << 13,
    DEVICE_VALID_ECC_CORRECTED_AGGREGATE = 1ULL << 14,
    DEVICE_VALID_ECC_UNCORRECTED_AGGREGATE = 1ULL << 15
};

enum process_source {
    PROCESS_SOURCE_COMPUTE = 1U << 0,
    PROCESS_SOURCE_GRAPHICS = 1U << 1,
    PROCESS_SOURCE_MPS = 1U << 2
};

struct device_metadata {
    unsigned int index;
    char uuid[96];
    char name[128];
    char pci_bus_id[NVML_DEVICE_PCI_BUS_ID_BUFFER_SIZE];
    char driver_version[96];
    uint64_t valid_fields;
    nvmlReturn_t nvml_error;
};

struct device_snapshot {
    char uuid[96];
    unsigned int index;
    unsigned int gpu_util;
    unsigned int mem_util;
    unsigned long long mem_used;
    unsigned long long mem_free;
    unsigned long long mem_total;
    unsigned int temp_c;
    unsigned int power_mw;
    unsigned int sm_clock_mhz;
    unsigned int mem_clock_mhz;
    unsigned long long throttle_reasons;
    unsigned int power_limit_mw;
    unsigned long long ecc_corrected_volatile;
    unsigned long long ecc_uncorrected_volatile;
    unsigned long long ecc_corrected_aggregate;
    unsigned long long ecc_uncorrected_aggregate;
    uint64_t valid_fields;
    int valid;
    nvmlReturn_t nvml_error;
};

struct process_entry {
    unsigned int pid;
    unsigned long long used_gpu_memory;
    unsigned int source_flags;
    int memory_valid;
};

struct process_snapshot {
    unsigned int count;
    struct process_entry *entries;
    int valid;
    int complete;  /* false if any supported source query failed */
    int truncated; /* set by bounded consumers that cannot return every entry */
    nvmlReturn_t nvml_error;
    nvmlReturn_t compute_error;
    nvmlReturn_t graphics_error;
    nvmlReturn_t mps_error;
};

nvmlReturn_t poller_init(void);
void poller_shutdown(void);
void poller_nvml_lock(void);
void poller_nvml_unlock(void);
nvmlReturn_t poller_device_count(unsigned int *count);
void poller_device_metadata(unsigned int index, struct device_metadata *out);
void poller_snapshot_device_uuid(const char *uuid, struct device_snapshot *out);
void poller_snapshot_processes_uuid(const char *uuid, struct process_snapshot *out);
void poller_process_snapshot_destroy(struct process_snapshot *snapshot);
const struct process_entry *poller_find_pid(const struct process_snapshot *snap,
                                            unsigned int pid);
int poller_gpu_memory_value_valid(unsigned long long value);

#endif
