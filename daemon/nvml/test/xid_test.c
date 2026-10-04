#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <sys/types.h>

#include "../xid.h"

#define PASS 0
#define FAIL 1
#define SKIP 77

static int test_xid_event_plumbing(int manual_mode)
{
    struct xid_event_set *set = xid_event_set_create();
    if (!set) {
        fprintf(stderr, "FAIL: cannot allocate Xid event set\n");
        return FAIL;
    }
    if (!set->valid) {
        if (set->nvml_error == NVML_ERROR_NOT_SUPPORTED ||
            set->nvml_error == NVML_ERROR_NOT_FOUND) {
            printf("SKIP: Xid registration unsupported (NVML error %d)\n",
                   set->nvml_error);
            xid_event_set_destroy(set);
            return SKIP;
        }
        fprintf(stderr, "FAIL: event set registration: %s\n",
                nvmlErrorString((nvmlReturn_t)set->nvml_error));
        xid_event_set_destroy(set);
        return FAIL;
    }

    printf("TEST Xid event set registered for %u enumerated GPU(s)\n", set->device_count);
    struct xid_event event;
    int result;
    do {
        memset(&event, 0, sizeof(event));
        result = xid_event_set_wait(set, &event, manual_mode ? 1000 : 500);
        if (result == XID_WAIT_TIMEOUT && manual_mode)
            continue;
        break;
    } while (1);

    if (result == XID_WAIT_NOT_SUPPORTED) {
        printf("SKIP: Xid event registration unsupported\n");
        xid_event_set_destroy(set);
        return SKIP;
    }
    if (result == XID_WAIT_ERROR) {
        fprintf(stderr, "FAIL: Xid wait: %s\n",
                nvmlErrorString((nvmlReturn_t)set->nvml_error));
        xid_event_set_destroy(set);
        return FAIL;
    }
    if (result == XID_WAIT_TIMEOUT) {
        printf("PASS Xid event plumbing (clean timeout)\n");
        xid_event_set_destroy(set);
        return PASS;
    }

    if (!event.timestamp_ns || !event.device_uuid[0]) {
        fprintf(stderr, "FAIL: event missing timestamp or device UUID\n");
        xid_event_set_destroy(set);
        return FAIL;
    }
    if (event.nvml_event_type != nvmlEventTypeXidCriticalError) {
        fprintf(stderr, "FAIL: unexpected NVML event type %llu\n",
                event.nvml_event_type);
        xid_event_set_destroy(set);
        return FAIL;
    }
    printf("CAPTURED Xid: ts=%llu device=%u uuid=%s type=%llu xid=%llu\n",
           (unsigned long long)event.timestamp_ns, event.device_index,
           event.device_uuid, event.nvml_event_type, event.nvml_event_data);
    xid_event_set_destroy(set);
    return PASS;
}

int main(int argc, char **argv)
{
    int manual = 0;
    for (int i = 1; i < argc; i++)
        if (strcmp(argv[i], "--manual") == 0)
            manual = 1;

    if (geteuid() != 0 || access("/dev/nvidiactl", F_OK) != 0) {
        printf("SKIP: root and an NVIDIA device are required\n");
        return SKIP;
    }
    nvmlReturn_t result = nvmlInit();
    if (result != NVML_SUCCESS) {
        printf("SKIP: NVML init: %s\n", nvmlErrorString(result));
        return SKIP;
    }

    int rc = test_xid_event_plumbing(manual);
    nvmlShutdown();
    printf("\nxid_test: %s\n",
           rc == PASS ? "PASS" : (rc == SKIP ? "SKIP" : "FAIL"));
    return rc;
}
