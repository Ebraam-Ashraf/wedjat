package db

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// procKey uniquely identifies a process instance.
type procKey struct {
	Tgid       uint32
	StartTicks int64
}

// ReadProcessIdentity returns a process's start time in clock ticks since boot
// and its command name. It reads /proc/[pid]/stat directly.
//
// Field layout follows proc(5): the comm field (field 2) is parenthesised and
// may itself contain spaces and parentheses. starttime is field 22 (index 19
// in the post-comm slice).
func ReadProcessIdentity(pid uint) (int64, string, error) {
	data, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/stat")
	if err != nil {
		return 0, "", err
	}

	stat := string(data)
	open := strings.IndexByte(stat, '(')
	closing := strings.LastIndexByte(stat, ')')
	if open < 0 || closing < open {
		return 0, "", fmt.Errorf("proc %d: malformed stat line", pid)
	}

	command := stat[open+1 : closing]
	fields := strings.Fields(stat[closing+1:])
	const startTimeField = 22
	if len(fields) <= startTimeField-3 {
		return 0, "", fmt.Errorf("proc %d: stat line has %d fields after comm", pid, len(fields))
	}

	startTicks, err := strconv.ParseInt(fields[startTimeField-3], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("proc %d: parse start time: %w", pid, err)
	}
	return startTicks, command, nil
}

// resolveCUDAOrdinals maps CUDA-visible ordinals to GPU database IDs for a process.
// It reads the process environment and resolves based on CUDA device selection.
func resolveCUDAOrdinals(environ []byte, devices []gpuIdentity) map[uint32]int64 {
	result := make(map[uint32]int64)
	if len(devices) == 0 {
		return result
	}
	vars := make(map[string]string)
	for _, entry := range strings.Split(string(environ), "\x00") {
		if i := strings.IndexByte(entry, '='); i > 0 {
			vars[entry[:i]] = entry[i+1:]
		}
	}
	visible, hasVisible := vars["CUDA_VISIBLE_DEVICES"]
	if hasVisible && visible == "" {
		return result
	}
	ordered := append([]gpuIdentity(nil), devices...)
	if vars["CUDA_DEVICE_ORDER"] == "PCI_BUS_ID" {
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].device.PCIBusID < ordered[j].device.PCIBusID })
	}
	if hasVisible {
		for ordinal, token := range strings.Split(visible, ",") {
			token = strings.TrimSpace(token)
			for _, gpu := range devices {
				if token != "" && gpu.device.UUID == token {
					result[uint32(ordinal)] = gpu.id
					token = ""
					break
				}
			}
			if token == "" {
				continue
			}
			idx, err := strconv.Atoi(token)
			if err != nil || idx < 0 {
				continue
			}
			if len(devices) == 1 {
				result[uint32(ordinal)] = devices[0].id
			} else if vars["CUDA_DEVICE_ORDER"] == "PCI_BUS_ID" && idx < len(ordered) {
				result[uint32(ordinal)] = ordered[idx].id
			}
		}
		return result
	}
	if len(devices) == 1 {
		result[0] = devices[0].id
	} else if vars["CUDA_DEVICE_ORDER"] == "PCI_BUS_ID" {
		for i, gpu := range ordered {
			result[uint32(i)] = gpu.id
		}
	}
	return result
}

// startTicksMatchBoottime checks if process start ticks match the eBPF event's boottime.
func startTicksMatchBoottime(startBoottimeNs uint64, startTicks int64) bool {
	if startBoottimeNs == 0 || startTicks == 0 {
		return false
	}
	// Convert nanoseconds to ticks (assuming 100 ticks per second)
	expectedTicks := int64(startBoottimeNs / 10_000_000)
	return startTicks == expectedTicks
}

// gpuIdentity holds GPU info and database ID for resolution.
type gpuIdentity struct {
	device source.DeviceInfo
	id     int64
}

// RegisterDevices records GPU identities and returns a UUID to database ID mapping.
func (db *DB) RegisterDevices(ctx context.Context, devices []source.DeviceInfo) (map[string]int64, error) {
	result := make(map[string]int64)
	now := time.Now().Unix()

	for _, device := range devices {
		if device.UUID == "" {
			continue
		}

		identity := GPUIdentity{
			UUID:          device.UUID,
			Index:         int64(device.Index),
			Name:          device.Name,
			PCIBusID:      device.PCIBusID,
			DriverVersion: device.DriverVersion,
			SeenAtUnix:    now,
		}
		if device.VRAMValid {
			total := int64(device.VRAMTotal)
			identity.VRAMTotalBytes = &total
		}

		id, err := db.UpsertGPU(ctx, identity)
		if err != nil {
			return nil, err
		}
		result[device.UUID] = id
	}

	return result, nil
}
