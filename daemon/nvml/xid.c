#include <stddef.h>
#include <string.h>
#include <nvml.h>

struct xid_event {
    unsigned long long timestamp_ns;
    unsigned int       device_index;
    char               device_uuid[96];

    unsigned long long nvml_event_type;
    unsigned long long nvml_event_data;

    unsigned int       associated_pid;
};

struct xid_event_set {
    int            valid;
    int            nvml_error;
    nvmlEventSet_t set;
    unsigned int   device_index;
};

#define XID_WAIT_TIMEOUT        1
#define XID_WAIT_ERROR          2
#define XID_WAIT_NOT_SUPPORTED  3

struct xid_event_set *xid_event_set_create(unsigned int device_index)
{
    static struct xid_event_set s_set;
    memset(&s_set, 0, sizeof(s_set));

    nvmlEventSet_t eventSet;
    nvmlReturn_t r = nvmlEventSetCreate(&eventSet);
    if (r != NVML_SUCCESS) {
        s_set.nvml_error = (int)r;
        return &s_set;
    }

    nvmlDevice_t device;
    r = nvmlDeviceGetHandleByIndex(device_index, &device);
    if (r != NVML_SUCCESS) {
        nvmlEventSetFree(eventSet);
        s_set.nvml_error = (int)r;
        return &s_set;
    }

    r = nvmlDeviceRegisterEvents(device, nvmlEventTypeXidCriticalError, eventSet);
    if (r != NVML_SUCCESS) {
        nvmlEventSetFree(eventSet);
        s_set.nvml_error = (int)r;
        return &s_set;
    }

    s_set.set          = eventSet;
    s_set.device_index = device_index;
    s_set.valid        = 1;
    return &s_set;
}

int xid_event_set_wait(struct xid_event_set *set,
                       struct xid_event *event_out,
                       unsigned int timeout_ms)
{
    if (!set->valid)
        return XID_WAIT_NOT_SUPPORTED;

    nvmlEventData_t data;
    nvmlReturn_t result = nvmlEventSetWait(set->set, &data, timeout_ms);
    if (result == NVML_ERROR_TIMEOUT)
        return XID_WAIT_TIMEOUT;
    if (result != NVML_SUCCESS)
        return XID_WAIT_ERROR;

    memset(event_out, 0, sizeof(*event_out));
    event_out->device_index     = set->device_index;
    event_out->nvml_event_type  = data.eventType;
    event_out->nvml_event_data  = data.eventData;
    nvmlDeviceGetUUID(data.device, event_out->device_uuid, sizeof(event_out->device_uuid));
    event_out->associated_pid   = 0;

    return 0;
}

int xid_handle_event(const struct xid_event *event)
{
    (void)event;
    return 0;
}

void xid_event_set_destroy(struct xid_event_set *set)
{
    if (!set || !set->valid)
        return;
    nvmlEventSetFree(set->set);
    set->valid = 0;
}
