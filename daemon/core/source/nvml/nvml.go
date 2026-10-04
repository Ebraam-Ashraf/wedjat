// Package nvml wraps the NVIDIA Management Library via CGo.
//
// This file contains only the raw CGo bindings: types, validity-bit constants,
// and the thin Go functions that call into the C bridge. Higher-level concerns
// (adaptive polling, the NVMLSampler) live in run.go so this file can be
// reasoned about and reviewed in isolation.
package nvml

/*
#cgo CFLAGS: -I. -I../../../nvml -I/usr/local/cuda/include -Wno-deprecated-declarations -pthread
#cgo LDFLAGS: -ldl -pthread

#include "bridge.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"time"
	"unsafe"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// InitSources initializes the NVML library. Must be called once before any
// other function in this package.
func InitSources() error {
	if rc := C.collector_init(); rc != 0 {
		return fmt.Errorf("collector_init failed with code %d", int(rc))
	}
	return nil
}

// ShutdownSources releases NVML resources.
func ShutdownSources() {
	C.collector_shutdown()
}

// GetDeviceCount returns the number of NVIDIA GPUs visible to NVML.
func GetDeviceCount() (int, error) {
	var nvmlErr C.int
	count := C.collector_device_count(&nvmlErr)
	if count < 0 {
		return 0, fmt.Errorf("failed to get device count (NVML error %d)", int(nvmlErr))
	}
	return int(count), nil
}

// DiscoverDevices enumerates every GPU attached to this machine.
func DiscoverDevices() ([]source.DeviceInfo, error) {
	count, err := GetDeviceCount()
	if err != nil {
		return nil, err
	}
	devices := make([]source.DeviceInfo, 0, count)
	for i := 0; i < count; i++ {
		info, err := GetDeviceInfo(uint(i))
		if err != nil {
			return nil, err
		}
		devices = append(devices, info)
	}
	return devices, nil
}

// GetDeviceInfo returns the static identity of the device at index.
func GetDeviceInfo(index uint) (source.DeviceInfo, error) {
	var info C.struct_collector_device_info
	if rc := C.collector_device_info(C.uint(index), &info); rc != 0 {
		return source.DeviceInfo{}, fmt.Errorf("failed to get info for device %d (NVML error %d)",
			index, int(info.nvml_error))
	}
	return source.DeviceInfo{
		Index:         uint(info.index),
		UUID:          C.GoString(&info.uuid[0]),
		Name:          C.GoString(&info.name[0]),
		PCIBusID:      C.GoString(&info.pci_bus_id[0]),
		DriverVersion: C.GoString(&info.driver_version[0]),
		VRAMTotal:     uint64(info.vram_total_bytes),
		VRAMValid:     info.vram_valid != 0,
	}, nil
}

// PollGPU retrieves the current hardware telemetry for one GPU.
func PollGPU(uuid string) (source.GPUSample, error) {
	cUUID := C.CString(uuid)
	defer C.free(unsafe.Pointer(cUUID))

	var snap C.struct_collector_gpu_snap
	C.collector_snapshot_gpu(cUUID, &snap)

	if snap.ok == 0 {
		return source.GPUSample{}, fmt.Errorf("GPU snapshot failed for %s (NVML error %d)",
			uuid, int(snap.nvml_error))
	}

	now := time.Now()
	return source.GPUSample{
		TsNano:         now.UnixNano(),
		UUID:           uuid,
		UtilGPU:        uint(snap.gpu_util),
		UtilMem:        uint(snap.mem_util),
		MemUsed:        uint64(snap.mem_used),
		TempC:          uint(snap.temp_c),
		PowerMW:        uint(snap.power_mw),
		PowerLimitMW:   uint(snap.power_limit_mw),
		SMClockMHz:     uint(snap.sm_clock_mhz),
		MemClockMHz:    uint(snap.mem_clock_mhz),
		ThrottleReason: uint64(snap.throttle_reasons),
		ECCErrors:      uint64(snap.ecc_errors),
		ValidFields:    uint64(snap.valid_fields),
		Valid:          true,
	}, nil
}

// PollProcesses retrieves the per-process GPU usage list for one GPU.
func PollProcesses(uuid string) ([]source.ProcessSample, error) {
	cUUID := C.CString(uuid)
	defer C.free(unsafe.Pointer(cUUID))

	var list C.struct_collector_proc_list
	C.collector_snapshot_procs(cUUID, &list)

	if list.ok == 0 {
		return nil, fmt.Errorf("process snapshot failed for %s (NVML error %d; compute=%d graphics=%d mps=%d)",
			uuid, int(list.nvml_error), int(list.compute_error), int(list.graphics_error), int(list.mps_error))
	}

	procs := make([]source.ProcessSample, 0, int(list.count))
	for i := 0; i < int(list.count); i++ {
		entry := list.entries[i]
		procs = append(procs, source.ProcessSample{
			PID:       uint(entry.pid),
			GPUUUID:   uuid,
			VRAMBytes: uint64(entry.vram_bytes),
			VRAMValid: entry.vram_valid != 0,
		})
	}

	if list.complete == 0 {
		return procs, fmt.Errorf("process snapshot incomplete for %s", uuid)
	}
	return procs, nil
}

func createXidSet() unsafe.Pointer { return C.collector_xid_create() }

func destroyXidSet(set unsafe.Pointer) { C.collector_xid_destroy(set) }

func waitXid(set unsafe.Pointer, timeoutMs uint) (source.Xid, int) {
	var event C.struct_collector_xid_event
	result := int(C.collector_xid_wait(set, &event, C.uint(timeoutMs)))
	if result != 0 {
		return source.Xid{}, result
	}
	return source.Xid{TsNano: int64(event.timestamp_ns), UUID: C.GoString(&event.device_uuid[0]), Index: uint(event.device_index), Code: uint64(event.nvml_event_data)}, result
}
