/* xid_test.c
 *
 * Tests the Xid event plumbing in xid.c — not a real hardware fault.
 *
 * Structure:
 *   1. environment check  (GPU present)
 *   2. create event set   (xid_event_set_create)
 *   3. wait with timeout  (TIMEOUT = pass, NOT_SUPPORTED = skip)
 *
 * Manual mode (--manual): blocks until a real fault fires.  Never run in CI.
 *
 * usage:  ./bin/xid_test [--manual]
 * exit:   0 PASS  1 FAIL  77 SKIP
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "../xid.c"

#define PASS 0
#define FAIL 1
#define SKIP 77

/* ── assertion macros ────────────────────────────────────────────────────── */

#define ASSERT_TRUE(cond, msg)                                          \
    do {                                                                \
        if (!(cond)) {                                                  \
            fprintf(stderr, "FAIL [%s:%d]: %s\n",                      \
                    __FILE__, __LINE__, (msg));                         \
            return FAIL;                                                \
        }                                                               \
    } while (0)

/* ── test ────────────────────────────────────────────────────────────────── */

static int test_xid_event_plumbing(int manual_mode)
{
    unsigned int timeout_ms = manual_mode ? 0 : 500;
    printf("TEST xid_event_plumbing mode=%s timeout_ms=%u\n",
           manual_mode ? "manual" : "automated", timeout_ms);

    if (manual_mode)
        printf("  waiting for real hardware fault — Ctrl-C to abort\n");

    /* 2. create */
    struct xid_event_set *set = xid_event_set_create(0);
    if (!set) {
        fprintf(stderr, "FAIL: xid_event_set_create returned NULL\n");
        return FAIL;
    }

    if (!set->valid) {
        if (set->nvml_error == (int)NVML_ERROR_NOT_SUPPORTED) {
            printf("SKIP: Xid event registration not supported on this GPU/driver\n");
            xid_event_set_destroy(set);
            return SKIP;
        }
        fprintf(stderr, "FAIL: xid_event_set_create failed (nvml_error=%d: %s)\n",
                set->nvml_error, nvmlErrorString((nvmlReturn_t)set->nvml_error));
        xid_event_set_destroy(set);
        return FAIL;
    }

    /* 3. wait */
    struct xid_event event;
    memset(&event, 0, sizeof(event));
    int result = xid_event_set_wait(set, &event, timeout_ms);
    xid_event_set_destroy(set);

    if (result == XID_WAIT_NOT_SUPPORTED) {
        printf("SKIP: Xid event registration not supported on this GPU/driver\n");
        return SKIP;
    }

    if (result == XID_WAIT_ERROR) {
        fprintf(stderr, "FAIL: xid_event_set_wait returned error\n");
        return FAIL;
    }

    if (manual_mode) {
        ASSERT_TRUE(result == 0, "manual mode: expected a real fault event");
        printf("CAPTURED Xid fault:\n");
        printf("  device_index    = %u\n",              event.device_index);
        printf("  device_uuid     = %.40s...\n",        event.device_uuid);
        printf("  nvml_event_type = %llu\n",            (unsigned long long)event.nvml_event_type);
        printf("  nvml_event_data = %llu (Xid number)\n", (unsigned long long)event.nvml_event_data);
        printf("  associated_pid  = %u%s\n",            event.associated_pid,
               event.associated_pid == 0 ? " (device-wide or unknown)" : "");
        printf("PASS xid_event_plumbing (manual — real fault captured)\n");
        return PASS;
    }

    ASSERT_TRUE(result == XID_WAIT_TIMEOUT,
                "automated mode: expected TIMEOUT, got unexpected result");

    printf("PASS xid_event_plumbing (automated — clean timeout)\n");
    return PASS;
}

/* ── main ────────────────────────────────────────────────────────────────── */

int main(int argc, char *argv[])
{
    int manual_mode = 0;
    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--manual") == 0)
            manual_mode = 1;
    }

    printf("== xid_test ==\n");

    /* 1. environment */
    if (access("/dev/nvidiactl", F_OK) != 0) {
        printf("SKIP: no NVIDIA GPU\n");
        return SKIP;
    }

    if (nvmlInit() != NVML_SUCCESS) {
        printf("SKIP: nvmlInit failed\n");
        return SKIP;
    }

    int rc = test_xid_event_plumbing(manual_mode);

    nvmlShutdown();

    printf("\nxid_test: %s\n", rc == PASS ? "PASS" : (rc == SKIP ? "SKIP" : "FAIL"));
    return rc;
}
