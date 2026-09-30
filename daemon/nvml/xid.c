#include "xid.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static void keep_first_error(int *dst, nvmlReturn_t result)
{
    if (*dst == NVML_SUCCESS && result != NVML_SUCCESS)
        *dst = (int)result;
}

static nvmlReturn_t register_all_devices(struct xid_event_set *set)
{
    unsigned int count = 0;
    nvmlReturn_t result = nvmlDeviceGetCount(&count);

    if (result != NVML_SUCCESS)
        return result;

    free(set->device_uuids);
    set->device_uuids = NULL;
    set->device_count = 0;
    if (count == 0)
        return NVML_ERROR_NOT_FOUND;

    set->device_uuids = calloc(count, sizeof(*set->device_uuids));
    if (!set->device_uuids)
        return NVML_ERROR_MEMORY;

    unsigned int registered = 0;
    for (unsigned int index = 0; index < count; index++) {
        nvmlDevice_t device;
        char uuid[96] = {};
        result = nvmlDeviceGetHandleByIndex(index, &device);
        if (result != NVML_SUCCESS) {
            keep_first_error(&set->nvml_error, result);
            continue;
        }
        result = nvmlDeviceGetUUID(device, uuid, sizeof(uuid));
        if (result != NVML_SUCCESS) {
            keep_first_error(&set->nvml_error, result);
            continue;
        }
        result = nvmlDeviceRegisterEvents(device, nvmlEventTypeXidCriticalError,
                                          set->set);
        if (result != NVML_SUCCESS) {
            keep_first_error(&set->nvml_error, result);
            continue;
        }
        snprintf(set->device_uuids[registered], sizeof(set->device_uuids[registered]),
                 "%s", uuid);
        registered++;
    }
    set->device_count = registered;
    return registered ? NVML_SUCCESS :
           (set->nvml_error ? (nvmlReturn_t)set->nvml_error : NVML_ERROR_NOT_SUPPORTED);
}

struct xid_event_set *xid_event_set_create(void)
{
    struct xid_event_set *set = calloc(1, sizeof(*set));
    if (!set)
        return NULL;

    nvmlReturn_t result = nvmlEventSetCreate(&set->set);
    if (result != NVML_SUCCESS) {
        set->nvml_error = (int)result;
        return set;
    }
    set->set_created = 1;
    result = register_all_devices(set);
    set->valid = result == NVML_SUCCESS;
    keep_first_error(&set->nvml_error, result);
    return set;
}

static nvmlReturn_t recreate_set(struct xid_event_set *set)
{
    if (set->set_created) {
        nvmlEventSetFree(set->set);
        set->set_created = 0;
    }
    nvmlReturn_t result = nvmlShutdown();
    (void)result;
    result = nvmlInit();
    if (result != NVML_SUCCESS)
        return result;

    result = nvmlEventSetCreate(&set->set);
    if (result != NVML_SUCCESS)
        return result;
    set->set_created = 1;

    unsigned int registered = 0;
    for (unsigned int i = 0; i < set->device_count; i++) {
        nvmlDevice_t device;
        result = nvmlDeviceGetHandleByUUID(set->device_uuids[i], &device);
        if (result != NVML_SUCCESS)
            continue;
        result = nvmlDeviceRegisterEvents(device, nvmlEventTypeXidCriticalError,
                                          set->set);
        if (result == NVML_SUCCESS)
            registered++;
        else
            keep_first_error(&set->nvml_error, result);
    }
    set->valid = registered > 0;
    return set->valid ? NVML_SUCCESS : NVML_ERROR_NOT_SUPPORTED;
}

int xid_event_set_wait(struct xid_event_set *set, struct xid_event *event_out,
                       unsigned int timeout_ms)
{
    if (!set || !set->valid || !set->set_created)
        return XID_WAIT_NOT_SUPPORTED;
    if (!event_out) {
        set->nvml_error = NVML_ERROR_INVALID_ARGUMENT;
        return XID_WAIT_ERROR;
    }

    nvmlEventData_t data;
    nvmlReturn_t result = nvmlEventSetWait(set->set, &data, timeout_ms);
    if (result == NVML_ERROR_TIMEOUT)
        return XID_WAIT_TIMEOUT;
    if (result == NVML_ERROR_UNINITIALIZED || result == NVML_ERROR_DRIVER_NOT_LOADED) {
        set->nvml_error = (int)result;
        result = recreate_set(set);
        if (result != NVML_SUCCESS) {
            keep_first_error(&set->nvml_error, result);
            return XID_WAIT_ERROR;
        }
        return XID_WAIT_TIMEOUT;
    }
    if (result != NVML_SUCCESS) {
        set->nvml_error = (int)result;
        return XID_WAIT_ERROR;
    }

    memset(event_out, 0, sizeof(*event_out));
    struct timespec now;
    if (clock_gettime(CLOCK_REALTIME, &now) != 0) {
        set->nvml_error = NVML_ERROR_UNKNOWN;
        return XID_WAIT_ERROR;
    }
    event_out->timestamp_ns = (uint64_t)now.tv_sec * 1000000000ULL +
                              (uint64_t)now.tv_nsec;
    event_out->nvml_event_type = data.eventType;
    event_out->nvml_event_data = data.eventData;

    result = nvmlDeviceGetUUID(data.device, event_out->device_uuid,
                               sizeof(event_out->device_uuid));
    if (result != NVML_SUCCESS) {
        set->nvml_error = (int)result;
        return XID_WAIT_ERROR;
    }
    result = nvmlDeviceGetIndex(data.device, &event_out->device_index);
    if (result != NVML_SUCCESS) {
        set->nvml_error = (int)result;
        return XID_WAIT_ERROR;
    }
    return 0;
}

int xid_handle_event(const struct xid_event *event)
{
    (void)event;
    return 0;
}

void xid_event_set_destroy(struct xid_event_set *set)
{
    if (!set)
        return;
    if (set->set_created)
        nvmlEventSetFree(set->set);
    free(set->device_uuids);
    free(set);
}
