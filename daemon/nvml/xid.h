#ifndef WEDJAT_NVML_XID_H
#define WEDJAT_NVML_XID_H

#include <nvml.h>
#include <stdint.h>

struct xid_event {
    uint64_t timestamp_ns;
    unsigned int device_index;
    char device_uuid[96];
    unsigned long long nvml_event_type;
    unsigned long long nvml_event_data;
    /* PID of the process that triggered the Xid, if available from NVML.
     * Zero when NVML does not provide it (older driver / event type). */
    unsigned int associated_pid;
};

struct xid_event_set {
    int valid;
    int set_created;
    nvmlReturn_t nvml_error; /* NVML_SUCCESS (0) when no error */
    nvmlEventSet_t set;
    char (*device_uuids)[96];
    unsigned int device_count;
};

enum xid_wait_result {
    XID_WAIT_OK           = 0,
    XID_WAIT_TIMEOUT      = 1,
    XID_WAIT_ERROR        = 2,
    XID_WAIT_NOT_SUPPORTED = 3
};

struct xid_event_set *xid_event_set_create(void);
int xid_event_set_wait(struct xid_event_set *set, struct xid_event *event_out,
                       unsigned int timeout_ms);
int xid_handle_event(const struct xid_event *event);
void xid_event_set_destroy(struct xid_event_set *set);

#endif
