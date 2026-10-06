#include "xid.h"
#include "poller.h"
#include "nvml_loader.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static void keep_first_error(nvmlReturn_t *dst, nvmlReturn_t result) {
    if (*dst == NVML_SUCCESS && result != NVML_SUCCESS)
        *dst = result;
}

static nvmlReturn_t register_all_devices(struct xid_event_set *set) {
    unsigned int count = 0;
    nvmlReturn_t result = NVML_CALL(nvmlDeviceGetCount, &count);

    if (result != NVML_SUCCESS)
        return result;

    set->device_count = 0;
    if (count == 0)
        return NVML_ERROR_NOT_FOUND;

    unsigned int registered = 0;
    for (unsigned int index = 0; index < count; index++) {
        nvmlDevice_t device;
        result = NVML_CALL(nvmlDeviceGetHandleByIndex, index, &device);
        if (result != NVML_SUCCESS) {
            keep_first_error(&set->nvml_error, result);
            continue;
        }
        result = NVML_CALL(nvmlDeviceRegisterEvents, device,
                           nvmlEventTypeXidCriticalError, set->set);
        if (result != NVML_SUCCESS) {
            keep_first_error(&set->nvml_error, result);
            continue;
        }
        registered++;
    }

    set->device_count = registered;
    return registered ? NVML_SUCCESS
                      : (set->nvml_error ? set->nvml_error : NVML_ERROR_NOT_SUPPORTED);
}

static struct xid_event_set *xid_event_set_create_locked(void) {
    struct xid_event_set *set = calloc(1, sizeof(*set));
    if (!set)
        return NULL;

    nvmlReturn_t result = NVML_CALL(nvmlEventSetCreate, &set->set);
    if (result != NVML_SUCCESS) {
        set->nvml_error = result;
        return set;
    }
    set->set_created = 1;
    result = register_all_devices(set);
    set->valid = result == NVML_SUCCESS;
    keep_first_error(&set->nvml_error, result);
    return set;
}

struct xid_event_set *xid_event_set_create(void) {
    poller_nvml_lock();
    struct xid_event_set *set = xid_event_set_create_locked();
    poller_nvml_unlock();
    return set;
}

static nvmlReturn_t recreate_set(struct xid_event_set *set) {
    if (set->set_created) {
        NVML_CALL(nvmlEventSetFree, set->set);
        set->set_created = 0;
    }
    set->nvml_error = NVML_SUCCESS;

    nvmlReturn_t result = NVML_CALL(nvmlShutdown);
    (void)result;
    result = NVML_CALL(nvmlInit);
    if (result != NVML_SUCCESS)
        return result;

    result = NVML_CALL(nvmlEventSetCreate, &set->set);
    if (result != NVML_SUCCESS)
        return result;
    set->set_created = 1;

    result = register_all_devices(set);
    set->valid = result == NVML_SUCCESS;
    keep_first_error(&set->nvml_error, result);
    return result;
}

static int xid_event_set_wait_locked(struct xid_event_set *set,
                                     struct xid_event *event_out,
                                     unsigned int timeout_ms) {
    if (!set || !set->valid || !set->set_created)
        return XID_WAIT_NOT_SUPPORTED;
    if (!event_out) {
        set->nvml_error = NVML_ERROR_INVALID_ARGUMENT;
        return XID_WAIT_ERROR;
    }

    nvmlEventData_t data;
    nvmlReturn_t result = NVML_CALL(nvmlEventSetWait, set->set, &data, timeout_ms);
    if (result == NVML_ERROR_TIMEOUT)
        return XID_WAIT_TIMEOUT;
    if (result == NVML_ERROR_UNINITIALIZED || result == NVML_ERROR_DRIVER_NOT_LOADED) {
        set->nvml_error = result;
        result = recreate_set(set);
        if (result != NVML_SUCCESS) {
            keep_first_error(&set->nvml_error, result);
            return XID_WAIT_ERROR;
        }
        return XID_WAIT_TIMEOUT;
    }
    if (result != NVML_SUCCESS) {
        set->nvml_error = result;
        return XID_WAIT_ERROR;
    }

    memset(event_out, 0, sizeof(*event_out));
    struct timespec now;
    if (clock_gettime(CLOCK_REALTIME, &now) != 0) {
        set->nvml_error = NVML_ERROR_UNKNOWN;
        return XID_WAIT_ERROR;
    }
    event_out->timestamp_ns =
        (uint64_t)now.tv_sec * 1000000000ULL + (uint64_t)now.tv_nsec;
    event_out->nvml_event_type = data.eventType;
    event_out->nvml_event_data = data.eventData;

    result = NVML_CALL(nvmlDeviceGetUUID, data.device, event_out->device_uuid,
                       sizeof(event_out->device_uuid));
    if (result != NVML_SUCCESS) {
        set->nvml_error = result;
        return XID_WAIT_ERROR;
    }
    result = NVML_CALL(nvmlDeviceGetIndex, data.device, &event_out->device_index);
    if (result != NVML_SUCCESS) {
        set->nvml_error = result;
        return XID_WAIT_ERROR;
    }
    return XID_WAIT_OK;
}

int xid_event_set_wait(struct xid_event_set *set, struct xid_event *event_out,
                       unsigned int timeout_ms) {
    poller_nvml_lock();
    int result = xid_event_set_wait_locked(set, event_out, timeout_ms);
    poller_nvml_unlock();
    return result;
}

void xid_event_set_destroy(struct xid_event_set *set) {
    if (!set)
        return;

    poller_nvml_lock();
    if (set->set_created)
        NVML_CALL(nvmlEventSetFree, set->set);
    free(set);
    poller_nvml_unlock();
}
