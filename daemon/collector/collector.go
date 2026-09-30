package collector

/*
#cgo CFLAGS: -I../nvml -I../ebpf -I../ebpf/build -I/usr/local/cuda/include -I/usr/local/cuda/targets/x86_64-linux/include
#cgo LDFLAGS: -L/usr/lib/x86_64-linux-gnu -lnvidia-ml -lbpf -lelf -lz
#include "bridge.h"
*/
import "C"

import (
	"context"
	"log"

	"github.com/Ebraam-Ashraf/wedjat/daemon/bootstrap"
	"github.com/Ebraam-Ashraf/wedjat/daemon/store"
)

// Start initializes the collector, attaches eBPF probes, and starts background polling.
func Start(ctx context.Context, st *store.Store) (bootstrap.StopFunc, error) {
	log.Println("Starting collector stage...")
	
	res := C.collector_init()
	if res != 0 {
		log.Printf("Warning: collector_init returned %d (NVML might not be available)", res)
	}

	stopFunc := func(ctx context.Context) error {
		log.Println("Stopping collector stage...")
		C.collector_shutdown()
		return nil
	}

	return stopFunc, nil
}
