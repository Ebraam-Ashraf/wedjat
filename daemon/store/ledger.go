package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type GPUIdentity struct {
	UUID           string
	Index          *int64
	Name           string
	PCIBusID       string
	VRAMTotalBytes *int64
	DriverVersion  string
	ParentGPUID    *int64
	SeenAt         int64
}

type ProcessIdentity struct {
	BootID      string
	TGID        int64
	StartTicks  int64
	Command     string
	Cmdline     string
	Container   string
	FirstSeenAt int64
}

type ProcessGPUFlush struct {
	ProcessID   int64
	GPUID       int64
	FirstSeenAt int64
	LastSeenAt  int64
	PeakVRAM    *int64
	LastVRAM    *int64
	Launches    int64
	MemcpyBytes int64
	AllocBytes  int64
	FreeBytes   int64
	SyncCalls   int64
	WorstSyncUS int64
	Errors      int64
}

func (s *Store) UpsertGPU(ctx context.Context, gpu GPUIdentity) (int64, error) {
	if ctx == nil || strings.TrimSpace(gpu.UUID) == "" || gpu.UUID == "UNKNOWN" || gpu.SeenAt < 0 {
		return 0, errors.New("store: GPU UUID and non-negative seen timestamp are required")
	}
	if gpu.VRAMTotalBytes != nil && *gpu.VRAMTotalBytes < 0 {
		return 0, errors.New("store: negative GPU VRAM total")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.meta == nil {
		return 0, errors.New("store: write after close")
	}
	var gpuID int64
	err := s.meta.QueryRowContext(ctx, gpuIdentityUpsert,
		gpu.UUID, nullableInt(gpu.Index), gpu.Name, gpu.PCIBusID,
		nullableInt(gpu.VRAMTotalBytes), gpu.DriverVersion,
		nullableInt(gpu.ParentGPUID), gpu.SeenAt, gpu.SeenAt,
	).Scan(&gpuID)
	if err != nil {
		return 0, fmt.Errorf("upsert GPU %s: %w", gpu.UUID, err)
	}
	return gpuID, nil
}

func (s *Store) UpsertProcess(ctx context.Context, proc ProcessIdentity) (int64, error) {
	if ctx == nil || strings.TrimSpace(proc.BootID) == "" || proc.TGID <= 0 ||
		proc.StartTicks < 0 || proc.FirstSeenAt < 0 {
		return 0, errors.New("store: invalid process identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.meta == nil {
		return 0, errors.New("store: write after close")
	}
	var procID int64
	err := s.meta.QueryRowContext(ctx, processIdentityUpsert,
		proc.BootID, proc.TGID, proc.StartTicks, proc.Command,
		proc.Cmdline, proc.Container, proc.FirstSeenAt,
	).Scan(&procID)
	if err != nil {
		return 0, fmt.Errorf("upsert process %d: %w", proc.TGID, err)
	}
	return procID, nil
}

func (s *Store) MapProcessDevice(ctx context.Context, procID, ordinal, gpuID int64) error {
	if ctx == nil || procID <= 0 || ordinal < 0 || gpuID < 0 {
		return errors.New("store: invalid process-device mapping")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.meta == nil {
		return errors.New("store: write after close")
	}
	_, err := s.meta.ExecContext(ctx, "INSERT INTO proc_devices(proc_id, ordinal, gpu_id) VALUES (?, ?, ?) ON CONFLICT(proc_id, ordinal) DO NOTHING", procID, ordinal, gpuID)
	if err != nil {
		return err
	}
	var mappedGPUID int64
	if err := s.meta.QueryRowContext(ctx, "SELECT gpu_id FROM proc_devices WHERE proc_id = ? AND ordinal = ?", procID, ordinal).Scan(&mappedGPUID); err != nil {
		return err
	}
	if mappedGPUID != gpuID {
		return fmt.Errorf("process %d ordinal %d already maps to GPU %d, refusing remap to %d", procID, ordinal, mappedGPUID, gpuID)
	}
	return nil
}

func (s *Store) EndProcess(ctx context.Context, procID, endTS int64, reason string, exitCode, termSignal *int64) error {
	if ctx == nil || procID <= 0 || endTS < 0 || !validEndReason(reason) {
		return errors.New("store: invalid process exit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.meta == nil {
		return errors.New("store: write after close")
	}
	result, err := s.meta.ExecContext(ctx,
		"UPDATE procs SET end_ts = ?, end_reason = ?, exit_code = ?, term_signal = ? WHERE proc_id = ? AND end_ts IS NULL",
		endTS, reason, nullableInt(exitCode), nullableInt(termSignal), procID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("process %d is absent or already closed", procID)
	}
	return nil
}

func validEndReason(reason string) bool {
	switch reason {
	case "exit", "signal", "reboot", "daemon_gap", "vanished":
		return true
	default:
		return false
	}
}

// FlushProcessGPU commits ledger deltas and the heartbeat atomically.
func (s *Store) FlushProcessGPU(ctx context.Context, rows []ProcessGPUFlush, heartbeat time.Time) error {
	if ctx == nil {
		return errors.New("store: nil ledger flush context")
	}
	for i, row := range rows {
		if row.ProcessID < 0 || row.GPUID < 0 || row.FirstSeenAt < 0 ||
			row.LastSeenAt < row.FirstSeenAt {
			return fmt.Errorf("process GPU flush row %d has invalid identity or timestamps", i)
		}
		if (row.PeakVRAM != nil && *row.PeakVRAM < 0) ||
			(row.LastVRAM != nil && *row.LastVRAM < 0) {
			return fmt.Errorf("process GPU flush row %d has negative VRAM", i)
		}
		for _, counter := range []int64{
			row.Launches, row.MemcpyBytes, row.AllocBytes, row.FreeBytes,
			row.SyncCalls, row.WorstSyncUS, row.Errors,
		} {
			if counter < 0 {
				return fmt.Errorf("process GPU flush row %d has negative counter", i)
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.meta == nil {
		return errors.New("store: write after close")
	}
	tx, err := s.meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, processGPUUpsert)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx,
			row.ProcessID, row.GPUID, row.FirstSeenAt, row.LastSeenAt,
			nullableInt(row.PeakVRAM), nullableInt(row.LastVRAM),
			row.Launches, row.MemcpyBytes, row.AllocBytes, row.FreeBytes,
			row.SyncCalls, row.WorstSyncUS, row.Errors,
		); err != nil {
			stmt.Close()
			return fmt.Errorf("flush process GPU row: %w", err)
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	if err := putState(ctx, tx, "heartbeat_ts", strconv.FormatInt(heartbeat.UTC().Unix(), 10)); err != nil {
		return err
	}
	return tx.Commit()
}

const gpuIdentityUpsert = "INSERT INTO gpus (uuid, idx, name, pci_bus_id, vram_total_bytes, driver_version, parent_gpu_id, first_seen_ts, last_seen_ts) " +
	"VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?) " +
	"ON CONFLICT(uuid) DO UPDATE SET idx=COALESCE(excluded.idx,gpus.idx), name=COALESCE(excluded.name,gpus.name), " +
	"pci_bus_id=COALESCE(excluded.pci_bus_id,gpus.pci_bus_id), vram_total_bytes=COALESCE(excluded.vram_total_bytes,gpus.vram_total_bytes), " +
	"driver_version=COALESCE(excluded.driver_version,gpus.driver_version), parent_gpu_id=COALESCE(excluded.parent_gpu_id,gpus.parent_gpu_id), " +
	"first_seen_ts=MIN(gpus.first_seen_ts,excluded.first_seen_ts), last_seen_ts=MAX(gpus.last_seen_ts,excluded.last_seen_ts) RETURNING gpu_id"

const processIdentityUpsert = "INSERT INTO procs (boot_id, tgid, start_ticks, command, cmdline, container, first_seen_ts) " +
	"VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?) " +
	"ON CONFLICT(boot_id,tgid,start_ticks) DO UPDATE SET command=COALESCE(excluded.command,procs.command), " +
	"cmdline=COALESCE(excluded.cmdline,procs.cmdline), container=COALESCE(excluded.container,procs.container) RETURNING proc_id"

const processGPUUpsert = "INSERT INTO proc_gpu (proc_id, gpu_id, first_seen_ts, last_seen_ts, peak_vram_bytes, last_vram_bytes, launches, memcpy_bytes, alloc_bytes, free_bytes, sync_calls, worst_sync_us, errors) " +
	"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
	"ON CONFLICT(proc_id,gpu_id) DO UPDATE SET first_seen_ts=MIN(proc_gpu.first_seen_ts,excluded.first_seen_ts), " +
	"last_seen_ts=MAX(proc_gpu.last_seen_ts,excluded.last_seen_ts), " +
	"peak_vram_bytes=CASE WHEN excluded.peak_vram_bytes IS NULL THEN proc_gpu.peak_vram_bytes WHEN proc_gpu.peak_vram_bytes IS NULL THEN excluded.peak_vram_bytes ELSE MAX(proc_gpu.peak_vram_bytes,excluded.peak_vram_bytes) END, " +
	"last_vram_bytes=COALESCE(excluded.last_vram_bytes,proc_gpu.last_vram_bytes), " +
	"launches=proc_gpu.launches+excluded.launches, memcpy_bytes=proc_gpu.memcpy_bytes+excluded.memcpy_bytes, " +
	"alloc_bytes=proc_gpu.alloc_bytes+excluded.alloc_bytes, free_bytes=proc_gpu.free_bytes+excluded.free_bytes, " +
	"sync_calls=proc_gpu.sync_calls+excluded.sync_calls, worst_sync_us=MAX(proc_gpu.worst_sync_us,excluded.worst_sync_us), " +
	"errors=proc_gpu.errors+excluded.errors"
