package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GPUIdentity is the static description of a GPU. SeenAtUnix is the first time
// the daemon observed it; it is never moved forward by later upserts.
type GPUIdentity struct {
	UUID           string
	Index          int64
	Name           string
	PCIBusID       string
	DriverVersion  string
	VRAMTotalBytes *int64
	SeenAtUnix     int64
}

// ProcessIdentity identifies one process instance. A PID alone is not enough:
// the kernel reuses PIDs, so identity is the boot ID plus the process start
// time in clock ticks since boot.
type ProcessIdentity struct {
	BootID        string
	TGID          int64
	StartTicks    int64
	Command       string
	FirstSeenUnix int64
}

// ProcessVRAM is one observation of a process's VRAM use on one GPU.
type ProcessVRAM struct {
	ProcessID  int64
	GPUID      int64
	SeenAtUnix int64
	VRAMBytes  int64
}

// GPUMinuteSample is a single poll tick destined for one UTC minute bucket.
// Sums accumulate and extrema are merged by the upsert, so a minute is filled
// by writing every tick rather than by buffering in memory.
type GPUMinuteSample struct {
	GPUID int64

	UtilGPUSum   *int64
	UtilGPUMax   *int64
	UtilMemSum   *int64
	TempC        *int64
	PowerMW      *int64
	VRAMUsed     *int64
	SMClockMHz   *int64
	MemClockMHz  *int64
	PowerLimitMW *int64
	ThrottleOR   *int64
	ECCErrors    *int64
}

// UpsertGPU records a GPU and returns its database id.
func (db *DB) UpsertGPU(ctx context.Context, gpu GPUIdentity) (int64, error) {
	if strings.TrimSpace(gpu.UUID) == "" {
		return 0, errors.New("db: GPU UUID is required")
	}
	if gpu.SeenAtUnix < 0 {
		return 0, errors.New("db: negative GPU seen timestamp")
	}
	if gpu.VRAMTotalBytes != nil && *gpu.VRAMTotalBytes < 0 {
		return 0, errors.New("db: negative GPU VRAM total")
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	var gpuID int64
	if err := db.meta.QueryRowContext(ctx, gpuIdentityUpsert,
		gpu.UUID, gpu.Index, gpu.Name, gpu.PCIBusID,
		nullableInt(gpu.VRAMTotalBytes), gpu.DriverVersion,
		gpu.SeenAtUnix, gpu.SeenAtUnix,
	).Scan(&gpuID); err != nil {
		return 0, fmt.Errorf("upsert GPU %s: %w", gpu.UUID, err)
	}
	return gpuID, nil
}

// GPUIDByUUID returns the database id of a GPU, registering the device the
// first time it is seen. Looking the id up by UUID on every tick keeps callers
// stateless; only a GPU that appears after startup pays for the insert.
func (db *DB) GPUIDByUUID(ctx context.Context, uuid string) (int64, error) {
	if strings.TrimSpace(uuid) == "" {
		return 0, errors.New("db: GPU UUID is required")
	}

	db.mu.Lock()
	if err := db.checkOpen(); err != nil {
		db.mu.Unlock()
		return 0, err
	}
	var gpuID int64
	err := db.meta.QueryRowContext(ctx, `SELECT gpu_id FROM gpus WHERE uuid = ?`, uuid).Scan(&gpuID)
	db.mu.Unlock()

	if err == nil {
		return gpuID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("look up GPU %s: %w", uuid, err)
	}
	return db.UpsertGPU(ctx, GPUIdentity{UUID: uuid, SeenAtUnix: time.Now().Unix()})
}

// UpsertProcess records a process instance and returns its database id.
func (db *DB) UpsertProcess(ctx context.Context, proc ProcessIdentity) (int64, error) {
	if strings.TrimSpace(proc.BootID) == "" {
		return 0, errors.New("db: boot ID is required")
	}
	if proc.TGID <= 0 {
		return 0, errors.New("db: process TGID must be positive")
	}
	if proc.StartTicks < 0 {
		return 0, errors.New("db: negative process start ticks")
	}
	if proc.FirstSeenUnix < 0 {
		return 0, errors.New("db: negative process seen timestamp")
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	var procID int64
	if err := db.meta.QueryRowContext(ctx, processIdentityUpsert,
		proc.BootID, proc.TGID, proc.StartTicks, proc.Command, proc.FirstSeenUnix,
	).Scan(&procID); err != nil {
		return 0, fmt.Errorf("upsert process %d: %w", proc.TGID, err)
	}
	return procID, nil
}

// UpsertProcessVRAM updates the running VRAM ledger for process/GPU pairs.
// Peak VRAM only ever grows, so a GPU that frees memory still shows what it
// held at its high-water mark.
func (db *DB) UpsertProcessVRAM(ctx context.Context, rows []ProcessVRAM) error {
	for i, row := range rows {
		if row.ProcessID < 0 || row.GPUID < 0 {
			return fmt.Errorf("process VRAM row %d has a negative identity", i)
		}
		if row.SeenAtUnix < 0 || row.VRAMBytes < 0 {
			return fmt.Errorf("process VRAM row %d has a negative value", i)
		}
	}
	if len(rows) == 0 {
		return nil
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return err
	}

	tx, err := db.meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, processVRAMUpsert)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx,
			row.ProcessID, row.GPUID, row.SeenAtUnix, row.SeenAtUnix,
			row.VRAMBytes); err != nil {
			stmt.Close()
			return fmt.Errorf("upsert process VRAM: %w", err)
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

// WriteGPUMinute folds one poll tick into the minute bucket for at. The daily
// database is rotated first, so a daemon left running across midnight starts
// writing to the new day's file instead of yesterday's.
func (db *DB) WriteGPUMinute(ctx context.Context, at time.Time, samples []GPUMinuteSample) error {
	if len(samples) == 0 {
		return nil
	}
	for i, sample := range samples {
		if sample.GPUID < 0 {
			return fmt.Errorf("GPU sample %d has a negative GPU id", i)
		}
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return err
	}

	utc := at.UTC()
	if err := db.rotateDayDB(ctx, utc.Format(dayLayout)); err != nil {
		return err
	}
	minute := utc.Truncate(time.Minute).Unix()

	tx, err := db.day.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, gpuSampleUpsert)
	if err != nil {
		return err
	}
	for _, sample := range samples {
		if _, err := stmt.ExecContext(ctx,
			minute, sample.GPUID,
			nullableInt(sample.UtilGPUSum), nullableInt(sample.UtilGPUMax),
			nullableInt(sample.UtilMemSum), nullableInt(sample.TempC),
			nullableInt(sample.PowerMW), nullableInt(sample.VRAMUsed),
			nullableInt(sample.SMClockMHz), nullableInt(sample.MemClockMHz),
			nullableInt(sample.PowerLimitMW), nullableInt(sample.ThrottleOR),
			nullableInt(sample.ECCErrors),
		); err != nil {
			stmt.Close()
			return fmt.Errorf("upsert GPU minute sample: %w", err)
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

const gpuIdentityUpsert = "INSERT INTO gpus (uuid, idx, name, pci_bus_id, vram_total_bytes, driver_version, first_seen_ts, last_seen_ts) " +
	"VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?) " +
	"ON CONFLICT(uuid) DO UPDATE SET idx=excluded.idx, " +
	"name=COALESCE(excluded.name, gpus.name), " +
	"pci_bus_id=COALESCE(excluded.pci_bus_id, gpus.pci_bus_id), " +
	"vram_total_bytes=COALESCE(excluded.vram_total_bytes, gpus.vram_total_bytes), " +
	"driver_version=COALESCE(excluded.driver_version, gpus.driver_version), " +
	"first_seen_ts=MIN(gpus.first_seen_ts, excluded.first_seen_ts), " +
	"last_seen_ts=MAX(gpus.last_seen_ts, excluded.last_seen_ts) RETURNING gpu_id"

const processIdentityUpsert = "INSERT INTO procs (boot_id, tgid, start_ticks, command, first_seen_ts) " +
	"VALUES (?, ?, ?, NULLIF(?, ''), ?) " +
	"ON CONFLICT(boot_id, tgid, start_ticks) DO UPDATE SET " +
	"command=COALESCE(excluded.command, procs.command) RETURNING proc_id"

const processVRAMUpsert = "INSERT INTO proc_gpu (proc_id, gpu_id, first_seen_ts, last_seen_ts, last_vram_bytes) " +
	"VALUES (?, ?, ?, ?, ?) " +
	"ON CONFLICT(proc_id, gpu_id) DO UPDATE SET " +
	"last_seen_ts=MAX(proc_gpu.last_seen_ts, excluded.last_seen_ts), " +
	"peak_vram_bytes=CASE WHEN proc_gpu.peak_vram_bytes IS NULL THEN excluded.last_vram_bytes " +
	"WHEN excluded.last_vram_bytes IS NULL THEN proc_gpu.peak_vram_bytes " +
	"ELSE MAX(proc_gpu.peak_vram_bytes, excluded.last_vram_bytes) END, " +
	"last_vram_bytes=excluded.last_vram_bytes"

// gpuSampleUpsert merges one tick into an existing minute: sums add up,
// extrema widen, and a field that is unknown this tick (NULL) leaves the
// stored value untouched instead of overwriting it with nothing.
const gpuSampleUpsert = `INSERT INTO gpu_samples (
	ts, gpu_id, n, util_gpu_sum, util_gpu_max, util_mem_sum,
	temp_max, power_mw_sum, vram_used_max,
	sm_clock_min, mem_clock_min, power_limit_mw, throttle_or, ecc_errors
) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(ts, gpu_id) DO UPDATE SET
	n = gpu_samples.n + 1,
	util_gpu_sum = CASE WHEN excluded.util_gpu_sum IS NULL THEN gpu_samples.util_gpu_sum
		WHEN gpu_samples.util_gpu_sum IS NULL THEN excluded.util_gpu_sum
		ELSE gpu_samples.util_gpu_sum + excluded.util_gpu_sum END,
	util_gpu_max = CASE WHEN excluded.util_gpu_max IS NULL THEN gpu_samples.util_gpu_max
		WHEN gpu_samples.util_gpu_max IS NULL THEN excluded.util_gpu_max
		ELSE MAX(gpu_samples.util_gpu_max, excluded.util_gpu_max) END,
	util_mem_sum = CASE WHEN excluded.util_mem_sum IS NULL THEN gpu_samples.util_mem_sum
		WHEN gpu_samples.util_mem_sum IS NULL THEN excluded.util_mem_sum
		ELSE gpu_samples.util_mem_sum + excluded.util_mem_sum END,
	temp_max = CASE WHEN excluded.temp_max IS NULL THEN gpu_samples.temp_max
		WHEN gpu_samples.temp_max IS NULL THEN excluded.temp_max
		ELSE MAX(gpu_samples.temp_max, excluded.temp_max) END,
	power_mw_sum = CASE WHEN excluded.power_mw_sum IS NULL THEN gpu_samples.power_mw_sum
		WHEN gpu_samples.power_mw_sum IS NULL THEN excluded.power_mw_sum
		ELSE gpu_samples.power_mw_sum + excluded.power_mw_sum END,
	vram_used_max = CASE WHEN excluded.vram_used_max IS NULL THEN gpu_samples.vram_used_max
		WHEN gpu_samples.vram_used_max IS NULL THEN excluded.vram_used_max
		ELSE MAX(gpu_samples.vram_used_max, excluded.vram_used_max) END,
	sm_clock_min = CASE WHEN excluded.sm_clock_min IS NULL THEN gpu_samples.sm_clock_min
		WHEN gpu_samples.sm_clock_min IS NULL THEN excluded.sm_clock_min
		ELSE MIN(gpu_samples.sm_clock_min, excluded.sm_clock_min) END,
	mem_clock_min = CASE WHEN excluded.mem_clock_min IS NULL THEN gpu_samples.mem_clock_min
		WHEN gpu_samples.mem_clock_min IS NULL THEN excluded.mem_clock_min
		ELSE MIN(gpu_samples.mem_clock_min, excluded.mem_clock_min) END,
	power_limit_mw = COALESCE(excluded.power_limit_mw, gpu_samples.power_limit_mw),
	throttle_or = CASE WHEN excluded.throttle_or IS NULL THEN gpu_samples.throttle_or
		WHEN gpu_samples.throttle_or IS NULL THEN excluded.throttle_or
		ELSE gpu_samples.throttle_or | excluded.throttle_or END,
	ecc_errors = CASE WHEN excluded.ecc_errors IS NULL THEN gpu_samples.ecc_errors
		WHEN gpu_samples.ecc_errors IS NULL THEN excluded.ecc_errors
		ELSE MAX(gpu_samples.ecc_errors, excluded.ecc_errors) END`

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
