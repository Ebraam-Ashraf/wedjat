package nvml

/*
#cgo CFLAGS: -I../../nvml -I../../ebpf -I../../ebpf/build -I/usr/local/cuda/include -Wno-deprecated-declarations -pthread
#cgo LDFLAGS: -ldl -pthread

#include "bridge.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"unsafe"
)

// DeviceInfo holds the static identity of a GPU. It is read once at startup
// because these fields do not change while the machine is up.
type DeviceInfo struct {
	Index         uint
	UUID          string
	Name          string
	PCIBusID      string
	DriverVersion string
	VRAMTotal     uint64
	VRAMValid     bool
}

// Per-field availability bits for GPUSample.ValidFields. These mirror the
// NVML device_snapshot_field bits. A field whose bit is clear was not
// reported by the driver and must be stored as unknown, never as zero.
const (
	ValidGPUUtil        uint64 = 1 << 1
	ValidMemUtil        uint64 = 1 << 2
	ValidMemUsed        uint64 = 1 << 3
	ValidTemp           uint64 = 1 << 6
	ValidPower          uint64 = 1 << 7
	ValidSMClock        uint64 = 1 << 8
	ValidMemClock       uint64 = 1 << 9
	ValidThrottleReason uint64 = 1 << 10
	ValidPowerLimit     uint64 = 1 << 11
	ValidECCUncorrected uint64 = 1 << 13
)

// GPUSample represents a snapshot of GPU telemetry.
type GPUSample struct {
	UUID           string
	Index          uint
	UtilGPU        uint
	UtilMem        uint
	MemUsed        uint64
	TempC          uint
	PowerMW        uint
	PowerLimitMW   uint
	SMClockMHz     uint
	MemClockMHz    uint
	ThrottleReason uint64
	ECCErrors      uint64
	ValidFields    uint64
	Valid          bool
}

// ProcessSample represents per-process GPU usage. GPUUUID attributes the
// process to a device; without it multi-GPU hosts cannot be told apart.
type ProcessSample struct {
	PID       uint
	GPUUUID   string
	VRAMBytes uint64
	VRAMValid bool
}

// InitSources initializes NVML and eBPF subsystems.
func InitSources() error {
	rc := C.collector_init()
	if rc != 0 {
		return fmt.Errorf("collector_init failed with code %d", rc)
	}
	return nil
}

// ShutdownSources cleans up NVML and eBPF resources.
func ShutdownSources() {
	C.collector_shutdown()
}

// GetDeviceCount returns the number of NVIDIA GPUs.
func GetDeviceCount() (int, error) {
	var nvmlError C.int
	count := C.collector_device_count(&nvmlError)
	if count < 0 {
		return 0, fmt.Errorf("failed to get device count (NVML error %d)", int(nvmlError))
	}
	return int(count), nil
}

// GetDeviceUUID returns the UUID for a given device index.
func GetDeviceUUID(index uint) (string, error) {
	buf := make([]byte, 96)
	cBuf := (*C.char)(unsafe.Pointer(&buf[0]))
	var nvmlError C.int

	rc := C.collector_device_uuid(C.uint(index), cBuf, &nvmlError)
	if rc != 0 {
		return "", fmt.Errorf("failed to get UUID for device %d (NVML error %d)", index, int(nvmlError))
	}

	return C.GoString(cBuf), nil
}

// DiscoverDevices enumerates the GPUs attached to this machine. It is read
// once at startup, so a device that fails here is reported rather than skipped.
func DiscoverDevices() ([]DeviceInfo, error) {
	count, err := GetDeviceCount()
	if err != nil {
		return nil, err
	}

	devices := make([]DeviceInfo, 0, count)
	for i := 0; i < count; i++ {
		info, err := GetDeviceInfo(uint(i))
		if err != nil {
			return nil, err
		}
		devices = append(devices, info)
	}
	return devices, nil
}

// GetDeviceInfo returns the static identity of a device. Unavailable fields
// are left zeroed and must not be treated as real readings.
func GetDeviceInfo(index uint) (DeviceInfo, error) {
	var info C.struct_collector_device_info

	if rc := C.collector_device_info(C.uint(index), &info); rc != 0 {
		return DeviceInfo{}, fmt.Errorf("failed to get info for device %d (NVML error %d)",
			index, int(info.nvml_error))
	}

	return DeviceInfo{
		Index:         uint(info.index),
		UUID:          C.GoString(&info.uuid[0]),
		Name:          C.GoString(&info.name[0]),
		PCIBusID:      C.GoString(&info.pci_bus_id[0]),
		DriverVersion: C.GoString(&info.driver_version[0]),
		VRAMTotal:     uint64(info.vram_total_bytes),
		VRAMValid:     info.vram_valid != 0,
	}, nil
}

// PollGPU retrieves current telemetry for a specific GPU.
func PollGPU(uuid string) (GPUSample, error) {
	cUUID := C.CString(uuid)
	defer C.free(unsafe.Pointer(cUUID))

	var snap C.struct_collector_gpu_snap
	C.collector_snapshot_gpu(cUUID, &snap)

	if snap.ok == 0 {
		return GPUSample{}, fmt.Errorf("GPU snapshot failed for %s (NVML error %d)",
			uuid, int(snap.nvml_error))
	}

	return GPUSample{
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

// PollProcesses retrieves per-process GPU usage for a specific GPU.
func PollProcesses(uuid string) ([]ProcessSample, error) {
	cUUID := C.CString(uuid)
	defer C.free(unsafe.Pointer(cUUID))

	var list C.struct_collector_proc_list
	C.collector_snapshot_procs(cUUID, &list)

	if list.ok == 0 {
		return nil, fmt.Errorf("process snapshot failed for %s (NVML error %d; compute=%d graphics=%d mps=%d)",
			uuid, int(list.nvml_error), int(list.compute_error), int(list.graphics_error), int(list.mps_error))
	}

	procs := make([]ProcessSample, 0, int(list.count))
	for i := 0; i < int(list.count); i++ {
		entry := list.entries[i]
		procs = append(procs, ProcessSample{
			PID:       uint(entry.pid),
			GPUUUID:   uuid,
			VRAMBytes: uint64(entry.vram_bytes),
			VRAMValid: entry.vram_valid != 0,
		})
	}

	if list.complete == 0 {
		return procs, fmt.Errorf("process snapshot incomplete for %s (NVML error %d; compute=%d graphics=%d mps=%d; truncated=%t)",
			uuid, int(list.nvml_error), int(list.compute_error), int(list.graphics_error), int(list.mps_error),
			list.truncated != 0)
	}
	return procs, nil
}
