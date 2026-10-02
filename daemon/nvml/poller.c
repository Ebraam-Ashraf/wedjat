#include "poller.h"
#include "nvml_loader.h"

#include <limits.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef WEDJAT_NVML_TEST
extern int poller_test_fail_power_query;
#endif

static int nvml_needs_reinit(nvmlReturn_t result)
{
    return result == NVML_ERROR_UNINITIALIZED ||
           result == NVML_ERROR_DRIVER_NOT_LOADED;
}

static void poller_keep_first_error(nvmlReturn_t *dst, nvmlReturn_t result)
{
    if (*dst == NVML_SUCCESS && result != NVML_SUCCESS)
        *dst = result;
}

int poller_gpu_memory_value_valid(unsigned long long value)
{
    return value != ULLONG_MAX;
}

/* Serialize all NVML calls because recovery may shut down and reinitialize the
 * library while other callers hold device handles. */
static pthread_mutex_t nvml_api_mu = PTHREAD_MUTEX_INITIALIZER;

void poller_nvml_lock(void)
{
    pthread_mutex_lock(&nvml_api_mu);
}

void poller_nvml_unlock(void)
{
    pthread_mutex_unlock(&nvml_api_mu);
}

static nvmlReturn_t reinit_for_uuid(const char *uuid, nvmlDevice_t *device)
{
    nvmlReturn_t result;
    NVML_CALL(nvmlShutdown); /* ignore — we're reinitialising regardless */
    result = NVML_CALL(nvmlInit);
    if (result == NVML_SUCCESS)
        result = NVML_CALL(nvmlDeviceGetHandleByUUID, uuid, device);
    return result;
}

static nvmlReturn_t get_handle_by_uuid(const char *uuid, nvmlDevice_t *device)
{
    nvmlReturn_t result = NVML_CALL(nvmlDeviceGetHandleByUUID, uuid, device);
    if (nvml_needs_reinit(result)) {
        result = reinit_for_uuid(uuid, device);
        if (result == NVML_SUCCESS)
            return result;
    }
    return result;
}

#define UUID_QUERY(uuid, device, result, expression) do { \
    (result) = (expression); \
    if (nvml_needs_reinit(result)) { \
        nvmlReturn_t recovery = reinit_for_uuid((uuid), &(device)); \
        (result) = recovery == NVML_SUCCESS ? (expression) : recovery; \
    } \
} while (0)

nvmlReturn_t poller_init(void)
{
    poller_nvml_lock();
    nvmlReturn_t result = NVML_CALL(nvmlInit);
    poller_nvml_unlock();
    return result;
}

void poller_shutdown(void)
{
    poller_nvml_lock();
    NVML_CALL(nvmlShutdown);
    poller_nvml_unlock();
}

nvmlReturn_t poller_device_count(unsigned int *count)
{
    poller_nvml_lock();
    nvmlReturn_t result = NVML_CALL(nvmlDeviceGetCount, count);
    if (nvml_needs_reinit(result)) {
        NVML_CALL(nvmlShutdown);
        result = NVML_CALL(nvmlInit);
        if (result == NVML_SUCCESS)
            result = NVML_CALL(nvmlDeviceGetCount, count);
    }
    poller_nvml_unlock();
    return result;
}

void poller_device_metadata(unsigned int index, struct device_metadata *out)
{
    nvmlDevice_t device;
    nvmlPciInfo_t pci;
    nvmlReturn_t result;

    memset(out, 0, sizeof(*out));
    out->index = index;

    poller_nvml_lock();
    result = NVML_CALL(nvmlDeviceGetHandleByIndex, index, &device);
    if (nvml_needs_reinit(result)) {
        NVML_CALL(nvmlShutdown);
        result = NVML_CALL(nvmlInit);
        if (result == NVML_SUCCESS)
            result = NVML_CALL(nvmlDeviceGetHandleByIndex, index, &device);
    }
    if (result != NVML_SUCCESS) {
        out->nvml_error = result;
        poller_nvml_unlock();
        return;
    }

    result = NVML_CALL(nvmlDeviceGetUUID, device, out->uuid, sizeof(out->uuid));
    if (result == NVML_SUCCESS)
        out->valid_fields |= DEVICE_META_UUID;
    else
        poller_keep_first_error(&out->nvml_error, result);

    result = NVML_CALL(nvmlDeviceGetName, device, out->name, sizeof(out->name));
    if (result == NVML_SUCCESS)
        out->valid_fields |= DEVICE_META_NAME;
    else
        poller_keep_first_error(&out->nvml_error, result);

    memset(&pci, 0, sizeof(pci));
    result = NVML_CALL(nvmlDeviceGetPciInfo, device, &pci);
    if (result == NVML_SUCCESS) {
        snprintf(out->pci_bus_id, sizeof(out->pci_bus_id), "%s", pci.busId);
        out->valid_fields |= DEVICE_META_PCI_BUS_ID;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    result = NVML_CALL(nvmlSystemGetDriverVersion, out->driver_version,
                       sizeof(out->driver_version));
    if (result == NVML_SUCCESS)
        out->valid_fields |= DEVICE_META_DRIVER_VERSION;
    else
        poller_keep_first_error(&out->nvml_error, result);
    poller_nvml_unlock();
}

void poller_snapshot_device_uuid(const char *uuid, struct device_snapshot *out)
{
    nvmlDevice_t device;
    nvmlUtilization_t utilization;
    nvmlMemory_t memory;
    nvmlReturn_t result;
    unsigned int value;
    unsigned long long ecc;

    memset(out, 0, sizeof(*out));
    if (!uuid || !uuid[0]) {
        out->nvml_error = NVML_ERROR_INVALID_ARGUMENT;
        return;
    }
    snprintf(out->uuid, sizeof(out->uuid), "%s", uuid);

    poller_nvml_lock();
    result = get_handle_by_uuid(uuid, &device);
    if (result != NVML_SUCCESS) {
        out->nvml_error = result;
        poller_nvml_unlock();
        return;
    }
    out->valid = 1;

    UUID_QUERY(uuid, device, result, NVML_CALL(nvmlDeviceGetIndex, device, &out->index));
    if (result == NVML_SUCCESS)
        out->valid_fields |= DEVICE_VALID_INDEX;
    else
        poller_keep_first_error(&out->nvml_error, result);

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetUtilizationRates, device, &utilization));
    if (result == NVML_SUCCESS) {
        out->gpu_util = utilization.gpu;
        out->mem_util = utilization.memory;
        out->valid_fields |= DEVICE_VALID_GPU_UTIL | DEVICE_VALID_MEM_UTIL;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result, NVML_CALL(nvmlDeviceGetMemoryInfo, device, &memory));
    if (result == NVML_SUCCESS) {
        if (memory.used != ULLONG_MAX) {
            out->mem_used = memory.used;
            out->valid_fields |= DEVICE_VALID_MEM_USED;
        }
        if (memory.free != ULLONG_MAX) {
            out->mem_free = memory.free;
            out->valid_fields |= DEVICE_VALID_MEM_FREE;
        }
        if (memory.total != ULLONG_MAX) {
            out->mem_total = memory.total;
            out->valid_fields |= DEVICE_VALID_MEM_TOTAL;
        }
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetTemperature, device, NVML_TEMPERATURE_GPU, &value));
    if (result == NVML_SUCCESS) {
        out->temp_c = value;
        out->valid_fields |= DEVICE_VALID_TEMP;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

#ifdef WEDJAT_NVML_TEST
    if (poller_test_fail_power_query)
        result = NVML_ERROR_NOT_SUPPORTED;
    else
#endif
        UUID_QUERY(uuid, device, result,
                   NVML_CALL(nvmlDeviceGetPowerUsage, device, &value));
    if (result == NVML_SUCCESS) {
        out->power_mw = value;
        out->valid_fields |= DEVICE_VALID_POWER;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetClockInfo, device, NVML_CLOCK_SM, &value));
    if (result == NVML_SUCCESS) {
        out->sm_clock_mhz = value;
        out->valid_fields |= DEVICE_VALID_SM_CLOCK;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetClockInfo, device, NVML_CLOCK_MEM, &value));
    if (result == NVML_SUCCESS) {
        out->mem_clock_mhz = value;
        out->valid_fields |= DEVICE_VALID_MEM_CLOCK;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
#if NVML_API_VERSION >= 13
               NVML_CALL(nvmlDeviceGetCurrentClocksEventReasons, device,
                         &out->throttle_reasons));
#else
               NVML_CALL(nvmlDeviceGetCurrentClocksThrottleReasons, device,
                         &out->throttle_reasons));
#endif
    if (result == NVML_SUCCESS) {
        out->valid_fields |= DEVICE_VALID_THROTTLE_REASONS;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetPowerManagementLimit, device,
                         &out->power_limit_mw));
    if (result == NVML_SUCCESS) {
        out->valid_fields |= DEVICE_VALID_POWER_LIMIT;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetTotalEccErrors, device,
                         NVML_MEMORY_ERROR_TYPE_CORRECTED, NVML_VOLATILE_ECC, &ecc));
    if (result == NVML_SUCCESS) {
        out->ecc_corrected_volatile = ecc;
        out->valid_fields |= DEVICE_VALID_ECC_CORRECTED_VOLATILE;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetTotalEccErrors, device,
                         NVML_MEMORY_ERROR_TYPE_UNCORRECTED, NVML_VOLATILE_ECC, &ecc));
    if (result == NVML_SUCCESS) {
        out->ecc_uncorrected_volatile = ecc;
        out->valid_fields |= DEVICE_VALID_ECC_UNCORRECTED_VOLATILE;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetTotalEccErrors, device,
                         NVML_MEMORY_ERROR_TYPE_CORRECTED, NVML_AGGREGATE_ECC, &ecc));
    if (result == NVML_SUCCESS) {
        out->ecc_corrected_aggregate = ecc;
        out->valid_fields |= DEVICE_VALID_ECC_CORRECTED_AGGREGATE;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }

    UUID_QUERY(uuid, device, result,
               NVML_CALL(nvmlDeviceGetTotalEccErrors, device,
                         NVML_MEMORY_ERROR_TYPE_UNCORRECTED, NVML_AGGREGATE_ECC, &ecc));
    if (result == NVML_SUCCESS) {
        out->ecc_uncorrected_aggregate = ecc;
        out->valid_fields |= DEVICE_VALID_ECC_UNCORRECTED_AGGREGATE;
    } else {
        poller_keep_first_error(&out->nvml_error, result);
    }
    poller_nvml_unlock();
}

typedef nvmlReturn_t (*process_query_fn)(nvmlDevice_t, unsigned int *,
                                          nvmlProcessInfo_t *);

static nvmlReturn_t query_process_list(nvmlDevice_t device,
                                       process_query_fn query,
                                       nvmlProcessInfo_t **infos_out,
                                       unsigned int *count_out)
{
    if (!query)
        return NVML_ERROR_NOT_SUPPORTED;

    unsigned int needed = 0;
    nvmlReturn_t result = query(device, &needed, NULL);

    *infos_out = NULL;
    *count_out = 0;
    if (result == NVML_SUCCESS && needed == 0)
        return NVML_SUCCESS;
    if (result != NVML_ERROR_INSUFFICIENT_SIZE && result != NVML_SUCCESS)
        return result;

    for (int attempt = 0; attempt < 5; attempt++) {
        unsigned int capacity = needed ? needed : 1;
        nvmlProcessInfo_t *infos = calloc(capacity, sizeof(*infos));
        if (!infos)
            return NVML_ERROR_MEMORY;

        unsigned int returned = capacity;
        result = query(device, &returned, infos);
        if (result == NVML_SUCCESS) {
            *infos_out = infos;
            *count_out = returned;
            return NVML_SUCCESS;
        }
        free(infos);
        if (result != NVML_ERROR_INSUFFICIENT_SIZE)
            return result;
        /* Some NVML versions do not update `returned` on INSUFFICIENT_SIZE.
         * If returned did not grow beyond what we just tried, double the
         * capacity ourselves so the loop makes progress. */
        if (returned > capacity)
            needed = returned;
        else
            needed = capacity * 2;
    }
    return NVML_ERROR_INSUFFICIENT_SIZE;
}

static nvmlReturn_t query_processes_uuid(const char *uuid,
                                        process_query_fn query,
                                        struct process_snapshot *out,
                                        unsigned int source)
{
    nvmlDevice_t device;
    nvmlProcessInfo_t *infos = NULL;
    unsigned int count = 0;
    nvmlReturn_t result = get_handle_by_uuid(uuid, &device);

    if (result != NVML_SUCCESS)
        return result;
    result = query_process_list(device, query, &infos, &count);
    if (result == NVML_ERROR_UNINITIALIZED || result == NVML_ERROR_DRIVER_NOT_LOADED) {
        result = reinit_for_uuid(uuid, &device);
        if (result == NVML_SUCCESS)
            result = query_process_list(device, query, &infos, &count);
    }
    if (result != NVML_SUCCESS)
        return result;

    for (unsigned int i = 0; i < count; i++) {
        unsigned int j;
        for (j = 0; j < out->count; j++)
            if (out->entries[j].pid == infos[i].pid)
                break;

        if (j == out->count) {
            struct process_entry *grown =
                realloc(out->entries, (out->count + 1) * sizeof(*out->entries));
            if (!grown) {
                /* Partial entries already in `out` remain valid; the caller
                 * will see whatever was accumulated before this failure.
                 * Return the error so the caller records it, but do not free
                 * out->entries — that belongs to the caller via destroy(). */
                free(infos);
                return NVML_ERROR_MEMORY;
            }
            out->entries = grown;
            memset(&out->entries[out->count], 0, sizeof(*out->entries));
            out->entries[out->count].pid = infos[i].pid;
            out->count++;
        }

        struct process_entry *entry = &out->entries[j];
        entry->source_flags |= source;
        if (poller_gpu_memory_value_valid(infos[i].usedGpuMemory)) {
            if (!entry->memory_valid ||
                infos[i].usedGpuMemory > entry->used_gpu_memory)
                entry->used_gpu_memory = infos[i].usedGpuMemory;
            entry->memory_valid = 1;
        }
    }

    free(infos);
    return NVML_SUCCESS;
}

void poller_snapshot_processes_uuid(const char *uuid, struct process_snapshot *out)
{
    process_query_fn compute_query = NULL;
    process_query_fn graphics_query = NULL;
    process_query_fn mps_query = NULL;
    nvmlReturn_t compute;
    nvmlReturn_t graphics;
    nvmlReturn_t mps;

    memset(out, 0, sizeof(*out));
    if (!uuid || !uuid[0]) {
        out->nvml_error = NVML_ERROR_INVALID_ARGUMENT;
        return;
    }

    poller_nvml_lock();
    wedjat_nvml_resolve((void **)&compute_query,
                        NVML_SYMBOL(nvmlDeviceGetComputeRunningProcesses));
    wedjat_nvml_resolve((void **)&graphics_query,
                        NVML_SYMBOL(nvmlDeviceGetGraphicsRunningProcesses));
    wedjat_nvml_resolve((void **)&mps_query,
                        NVML_SYMBOL(nvmlDeviceGetMPSComputeRunningProcesses));

    compute = query_processes_uuid(uuid, compute_query, out, PROCESS_SOURCE_COMPUTE);
    out->compute_error = compute;
    graphics = query_processes_uuid(uuid, graphics_query, out, PROCESS_SOURCE_GRAPHICS);
    out->graphics_error = graphics;
    mps = query_processes_uuid(uuid, mps_query, out, PROCESS_SOURCE_MPS);
    out->mps_error = mps;

    out->valid = compute == NVML_SUCCESS || graphics == NVML_SUCCESS ||
                 mps == NVML_SUCCESS;
    out->complete = out->valid &&
                    (compute == NVML_SUCCESS || compute == NVML_ERROR_NOT_SUPPORTED) &&
                    (graphics == NVML_SUCCESS || graphics == NVML_ERROR_NOT_SUPPORTED) &&
                    (mps == NVML_SUCCESS || mps == NVML_ERROR_NOT_SUPPORTED);
    if (compute != NVML_SUCCESS && compute != NVML_ERROR_NOT_SUPPORTED)
        poller_keep_first_error(&out->nvml_error, compute);
    if (graphics != NVML_SUCCESS && graphics != NVML_ERROR_NOT_SUPPORTED)
        poller_keep_first_error(&out->nvml_error, graphics);
    if (mps != NVML_SUCCESS && mps != NVML_ERROR_NOT_SUPPORTED)
        poller_keep_first_error(&out->nvml_error, mps);
    if (!out->valid) {
        nvmlReturn_t compute_error = out->compute_error;
        nvmlReturn_t graphics_error = out->graphics_error;
        nvmlReturn_t mps_error = out->mps_error;
        nvmlReturn_t nvml_error = out->nvml_error;
        /* Free any partial entries that may have been allocated before a
         * mid-query failure.  poller_process_snapshot_destroy handles the
         * case where entries is NULL. */
        poller_process_snapshot_destroy(out);
        out->compute_error = compute_error;
        out->graphics_error = graphics_error;
        out->mps_error = mps_error;
        out->nvml_error = nvml_error != NVML_SUCCESS ? nvml_error :
                          (compute_error != NVML_SUCCESS ? compute_error :
                           (graphics_error != NVML_SUCCESS ? graphics_error : mps_error));
    }
    poller_nvml_unlock();
}

void poller_process_snapshot_destroy(struct process_snapshot *snapshot)
{
    if (!snapshot)
        return;
    free(snapshot->entries);
    memset(snapshot, 0, sizeof(*snapshot));
}

const struct process_entry *poller_find_pid(const struct process_snapshot *snap,
                                            unsigned int pid)
{
    for (unsigned int i = 0; i < snap->count; i++)
        if (snap->entries[i].pid == pid)
            return &snap->entries[i];
    return NULL;
}
