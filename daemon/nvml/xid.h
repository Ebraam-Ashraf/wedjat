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
    unsigned int associated_pid;
};

struct xid_event_set {
    int valid;
    int set_created;
    int nvml_error;
    nvmlEventSet_t set;
    char (*device_uuids)[96];
    unsigned int device_count;
};

#define XID_WAIT_TIMEOUT        1
#define XID_WAIT_ERROR          2
#define XID_WAIT_NOT_SUPPORTED  3

struct xid_event_set *xid_event_set_create(void);
int xid_event_set_wait(struct xid_event_set *set, struct xid_event *event_out,
                       unsigned int timeout_ms);
int xid_handle_event(const struct xid_event *event);
void xid_event_set_destroy(struct xid_event_set *set);

#endif
