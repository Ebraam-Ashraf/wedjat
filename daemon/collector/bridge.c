#include "bridge.h"
#include <stdio.h>
#include "../nvml/poller.c"
#define keep_first_error xid_keep_first_error
#include "../nvml/xid.c"
#undef keep_first_error

// Note: To truly integrate the eBPF skeletons, we'd include them here.
// e.g. #include "../ebpf/build/driver_kprobes.skel.h"
// For now, we will do a stub that calls NVML poller init.

int collector_init(void) {
    if (poller_init() != 0) {
        // Just return 0 in dev mode if NVML fails (e.g. no GPU) for now
        // But for production, this might be a real failure.
        return -1;
    }
    return 0;
}

void collector_shutdown(void) {
    poller_shutdown();
}
