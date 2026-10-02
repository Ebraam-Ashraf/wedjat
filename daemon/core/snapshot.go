package core

import (
	"os"
	"strings"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/nvml"
)

// BuildSnapshot creates a live snapshot from current GPU state. It polls every
// GPU named by gpuUUIDs and collects processes for each.
func BuildSnapshot(gpuUUIDs []string) Snapshot {
	now := time.Now()
	snapshot := Snapshot{
		UnixNano:   now.UnixNano(),
		MinuteUnix: now.Unix() / 60 * 60,
		GPUs:       make([]nvml.GPUSample, 0),
		Processes:  make([]nvml.ProcessSample, 0),
		// Cleared below if any device fails to report, so the daemon never
		// closes processes on the strength of a partial list.
		ProcessesComplete: true,
	}

	for i, uuid := range gpuUUIDs {
		if uuid == "" {
			continue
		}

		sample, err := nvml.PollGPU(uuid)
		if err != nil {
			continue
		}
		sample.Index = uint(i)
		snapshot.GPUs = append(snapshot.GPUs, sample)

		// Get processes for this GPU
		procs, err := nvml.PollProcesses(uuid)
		if err != nil {
			snapshot.ProcessesComplete = false
			continue
		}
		snapshot.Processes = append(snapshot.Processes, procs...)
	}

	return snapshot
}

// ReadBootID reads the system boot ID. The trailing newline is stripped so
// every table stores the same value; a verbatim read would silently break
// joins on boot_id.
func ReadBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
