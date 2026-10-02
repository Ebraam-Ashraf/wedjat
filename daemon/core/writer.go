package core

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/nvml"
)

// RegisterDevices records the static identity of every GPU. It is called once
// at startup, after which the per-tick path only needs the UUIDs.
func RegisterDevices(ctx context.Context, database *db.DB, devices []nvml.DeviceInfo) error {
	for _, device := range devices {
		if device.UUID == "" {
			log.Printf("RegisterDevices: device at index %d has an empty UUID, skipping (driver anomaly)", device.Index)
			continue
		}

		identity := db.GPUIdentity{
			UUID:          device.UUID,
			Index:         int64(device.Index),
			Name:          device.Name,
			PCIBusID:      device.PCIBusID,
			DriverVersion: device.DriverVersion,
			SeenAtUnix:    time.Now().Unix(),
		}
		if device.VRAMValid {
			total := int64(device.VRAMTotal)
			identity.VRAMTotalBytes = &total
		}

		if _, err := database.UpsertGPU(ctx, identity); err != nil {
			return err
		}
	}
	return nil
}

// Record writes one snapshot: GPU telemetry into the current minute bucket and
// per-process VRAM into the running ledger. A field the driver did not report
// is stored as unknown rather than as zero.
//
// NVML reports what a GPU is doing right now; the databases answer what it was
// doing over a minute or since a process started. This is the bridge: it
// resolves stable identities (GPU UUID, boot ID plus process start ticks) and
// folds a poll tick into the right bucket.
func Record(ctx context.Context, database *db.DB, bootID string, snapshot Snapshot) error {
	if len(snapshot.GPUs) == 0 && len(snapshot.Processes) == 0 {
		return nil
	}

	// The minute bucket comes from the snapshot's own minute so the stored
	// timestamp cannot drift from the one the socket broadcast.
	minute := time.Unix(snapshot.MinuteUnix, 0).UTC()
	now := time.Unix(0, snapshot.UnixNano).UTC()

	samples := make([]db.GPUMinuteSample, 0, len(snapshot.GPUs))
	for _, gpu := range snapshot.GPUs {
		if !gpu.Valid {
			continue
		}
		gpuID, err := database.GPUIDByUUID(ctx, gpu.UUID)
		if err != nil {
			return err
		}

		valid := gpu.ValidFields
		samples = append(samples, db.GPUMinuteSample{
			GPUID:        gpuID,
			UtilGPUSum:   reported(valid&nvml.ValidGPUUtil != 0, int64(gpu.UtilGPU)),
			UtilGPUMax:   reported(valid&nvml.ValidGPUUtil != 0, int64(gpu.UtilGPU)),
			UtilMemSum:   reported(valid&nvml.ValidMemUtil != 0, int64(gpu.UtilMem)),
			TempC:        reported(valid&nvml.ValidTemp != 0, int64(gpu.TempC)),
			PowerMW:      reported(valid&nvml.ValidPower != 0, int64(gpu.PowerMW)),
			VRAMUsed:     reported(valid&nvml.ValidMemUsed != 0, int64(gpu.MemUsed)),
			SMClockMHz:   reported(valid&nvml.ValidSMClock != 0, int64(gpu.SMClockMHz)),
			MemClockMHz:  reported(valid&nvml.ValidMemClock != 0, int64(gpu.MemClockMHz)),
			PowerLimitMW: reported(valid&nvml.ValidPowerLimit != 0, int64(gpu.PowerLimitMW)),
			ThrottleOR:   reported(valid&nvml.ValidThrottleReason != 0, int64(gpu.ThrottleReason)),
			ECCErrors:    reported(valid&nvml.ValidECCUncorrected != 0, int64(gpu.ECCErrors)),
		})
	}

	if err := database.WriteGPUMinute(ctx, minute, samples); err != nil {
		return err
	}
	return recordProcesses(ctx, database, bootID, now, snapshot)
}

// recordProcesses refreshes the VRAM ledger for every process NVML still sees,
// then closes the processes that have disappeared.
//
// A process stops being reported by NVML the moment it exits, and the daemon
// has no exit events of its own, so without this sweep every process row would
// stay open forever and look like it were still running.
func recordProcesses(ctx context.Context, database *db.DB, bootID string, at time.Time, snapshot Snapshot) error {
	if len(snapshot.Processes) == 0 && !snapshot.ProcessesComplete {
		// Both conditions must be true: an empty list that is also incomplete
		// means at least one device failed its poll entirely. There is nothing
		// to upsert, and nothing safe to close, so skip the sweep entirely.
		// (An empty list that IS complete is valid — all processes have exited —
		// and falls through so CloseProcessesNotSeen still runs the sweep.)
		return nil
	}

	seenAt := at.Unix()
	rows := make([]db.ProcessVRAM, 0, len(snapshot.Processes))
	seen := make([]int64, 0, len(snapshot.Processes))

	for _, proc := range snapshot.Processes {
		// A process can exit between the NVML listing and this read, so a
		// missing /proc entry is an ordinary race rather than a failure.
		startTicks, command, err := ReadProcessIdentity(proc.PID)
		if err != nil {
			continue
		}

		procID, err := database.UpsertProcess(ctx, db.ProcessIdentity{
			BootID:        bootID,
			TGID:          int64(proc.PID),
			StartTicks:    startTicks,
			Command:       command,
			FirstSeenUnix: seenAt,
		})
		if err != nil {
			return err
		}
		seen = append(seen, procID)

		gpuID, err := database.GPUIDByUUID(ctx, proc.GPUUUID)
		if err != nil {
			return err
		}

		// NVML reports NVML_VALUE_NOT_AVAILABLE for some processes. Storing
		// that sentinel would permanently poison the peak, so skip it.
		if !proc.VRAMValid {
			continue
		}
		rows = append(rows, db.ProcessVRAM{
			ProcessID:  procID,
			GPUID:      gpuID,
			SeenAtUnix: seenAt,
			VRAMBytes:  int64(proc.VRAMBytes),
		})
	}

	if err := database.UpsertProcessVRAM(ctx, rows); err != nil {
		return err
	}

	// Only sweep when the process list is known to be complete. A failed poll
	// would otherwise close processes that are alive but missing from it.
	if !snapshot.ProcessesComplete {
		return nil
	}
	if _, err := database.CloseProcessesNotSeen(ctx, bootID, seen, seenAt); err != nil {
		return err
	}
	return nil
}

// ReadProcessIdentity returns a process's start time in clock ticks since boot
// and its command name.
//
// Start ticks are what make a process row unique: the kernel recycles PIDs, so
// the same TGID can belong to unrelated processes over a daemon's lifetime.
//
// Field layout follows proc(5): the comm field (field 2) is parenthesised and
// may itself contain spaces and parentheses, so all field positions are counted
// from the character after the closing parenthesis. starttime is field 22, which
// sits at index 19 in the post-comm slice (22 - 3 = 19, because the slice
// starts at field 3). See proc(5) § /proc/[pid]/stat for the authoritative
// field numbering.
func ReadProcessIdentity(pid uint) (int64, string, error) {
	data, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/stat")
	if err != nil {
		return 0, "", err
	}

	stat := string(data)
	// comm (field 2) is parenthesised and may contain spaces and parentheses
	// itself, so all subsequent fields are counted from after its closing ')'.
	open := strings.IndexByte(stat, '(')
	closing := strings.LastIndexByte(stat, ')')
	if open < 0 || closing < open {
		return 0, "", fmt.Errorf("proc %d: malformed stat line", pid)
	}

	command := stat[open+1 : closing]
	fields := strings.Fields(stat[closing+1:])
	// fields[0] is field 3 (state); starttime is field 22, so index = 22-3 = 19.
	// See proc(5) § /proc/[pid]/stat.
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

// reported returns a pointer to value, or nil when the driver did not report
// the field. Callers store nil as SQL NULL so an unavailable reading is never
// mistaken for a real zero.
func reported(ok bool, value int64) *int64 {
	if !ok {
		return nil
	}
	return &value
}
