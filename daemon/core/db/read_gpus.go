package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"
)

// OpenDBReadOnly opens the metadata and daily databases in read-only mode.
// This is used by the HTTP server to serve queries without blocking the daemon.
func OpenDBReadOnly(ctx context.Context, dataDir string) (*DB, error) {
	if ctx == nil {
		return nil, errors.New("db: nil context")
	}

	db := &DB{root: dataDir}

	// Open metadata database read-only
	metaPath := filepath.Join(dataDir, "meta.db")
	var err error
	db.meta, err = openSQLiteReadOnly(ctx, metaPath)
	if err != nil {
		return nil, fmt.Errorf("open metadata database: %w", err)
	}

	// Open today's daily database read-only
	today := time.Now().UTC().Format(dayLayout)
	if err := db.rotateDayDBReadOnly(ctx, today); err != nil {
		db.meta.Close()
		return nil, fmt.Errorf("open daily database: %w", err)
	}

	return db, nil
}

// openSQLiteReadOnly opens a SQLite database in read-only mode with WAL.
func openSQLiteReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	// Use read-only mode with WAL for concurrent reads
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_sync=NORMAL&_busy_timeout=5000&mode=ro", path)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Allow multiple concurrent read-only connections
	database.SetMaxOpenConns(10)
	database.SetMaxIdleConns(5)

	// Verify connection
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, err
	}

	// Foreign keys should be ON for referential integrity
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		database.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	return database, nil
}

// rotateDayDBReadOnly opens the daily database for the given date in read-only mode.
func (db *DB) rotateDayDBReadOnly(ctx context.Context, date string) error {
	if db.dayName == date && db.day != nil {
		return nil
	}

	if db.day != nil {
		db.day.Close()
	}

	dayPath := filepath.Join(db.root, date+".db")
	var err error
	db.day, err = openSQLiteReadOnly(ctx, dayPath)
	if err != nil {
		return fmt.Errorf("open daily database: %w", err)
	}

	db.dayName = date
	return nil
}

// GPUWithStats includes GPU identity with computed stats.
type GPUWithStats struct {
	GPUIdentity
	CurrentUtilGPU  *int64 `json:"current_util_gpu,omitempty"`
	CurrentUtilMem  *int64 `json:"current_util_mem,omitempty"`
	CurrentTempC    *int64 `json:"current_temp_c,omitempty"`
	CurrentPowerMW  *int64 `json:"current_power_mw,omitempty"`
	CurrentVRAMUsed *int64 `json:"current_vram_used,omitempty"`
}

// ListGPUs returns all known GPUs with their latest telemetry.
func (db *DB) ListGPUs(ctx context.Context) ([]GPUWithStats, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	rows, err := db.meta.QueryContext(ctx, `
		SELECT gpu_id, uuid, idx, name, pci_bus_id, vram_total_bytes, driver_version, first_seen_ts, last_seen_ts
		FROM gpus
		ORDER BY idx
	`)
	if err != nil {
		log.Printf("ListGPUs: query error: %v", err)
		return nil, fmt.Errorf("list GPUs: %w", err)
	}
	defer rows.Close()

	var gpus []GPUWithStats
	for rows.Next() {
		var g GPUWithStats
		var vramTotal sql.NullInt64
		var pciBusID, driverVersion sql.NullString
		var gpuID int64
		if err := rows.Scan(&gpuID, &g.UUID, &g.Index, &g.Name, &pciBusID, &vramTotal, &driverVersion, &g.SeenAtUnix, &g.SeenAtUnix); err != nil {
			return nil, fmt.Errorf("scan GPU: %w", err)
		}
		if vramTotal.Valid {
			g.VRAMTotalBytes = &vramTotal.Int64
		}
		if pciBusID.Valid {
			g.PCIBusID = pciBusID.String
		}
		if driverVersion.Valid {
			g.DriverVersion = driverVersion.String
		}
		gpus = append(gpus, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate GPUs: %w", err)
	}

	// For each GPU, fetch the latest minute sample
	for i := range gpus {
		latest, err := db.latestGPUSample(ctx, int64(gpus[i].Index))
		if err != nil {
			return nil, err
		}
		if latest != nil {
			gpus[i].CurrentUtilGPU = latest.UtilGPUAvg
			gpus[i].CurrentUtilMem = latest.UtilMemAvg
			gpus[i].CurrentTempC = latest.TempMax
			gpus[i].CurrentPowerMW = latest.PowerMWAvg
			gpus[i].CurrentVRAMUsed = latest.VRAMUsedMax
		}
	}

	return gpus, nil
}

// GPUSampleMinute represents one minute of GPU telemetry.
type GPUSampleMinute struct {
	TS            int64   `json:"ts"`
	GPUID         int64   `json:"gpu_id"`
	N             int64   `json:"n"`
	UtilGPUAvg    *int64  `json:"util_gpu_avg,omitempty"`
	UtilGPUMax    *int64  `json:"util_gpu_max,omitempty"`
	UtilMemAvg    *int64  `json:"util_mem_avg,omitempty"`
	TempMax       *int64  `json:"temp_max,omitempty"`
	PowerMWAvg    *int64  `json:"power_mw_avg,omitempty"`
	VRAMUsedMax   *int64  `json:"vram_used_max,omitempty"`
	SMClockMax    *int64  `json:"sm_clock_max,omitempty"`
	MemClockMax   *int64  `json:"mem_clock_max,omitempty"`
	PowerLimitMW  *int64  `json:"power_limit_mw,omitempty"`
	ThrottleOR    *int64  `json:"throttle_or,omitempty"`
	ECCErrors     *int64  `json:"ecc_errors,omitempty"`
}

// latestGPUSample fetches the most recent minute sample for a GPU.
// Returns (nil, nil) if no sample data exists (not an error).
func (db *DB) latestGPUSample(ctx context.Context, gpuID int64) (*GPUSampleMinute, error) {
	// Try today's database first
	if db.day != nil {
		row := db.day.QueryRowContext(ctx, `
			SELECT ts, gpu_id, n, util_gpu_sum, util_gpu_max, util_mem_sum,
			       temp_max, power_mw_sum, vram_used_max,
			       sm_clock_max, mem_clock_max, power_limit_mw, throttle_or, ecc_errors
			FROM gpu_samples
			WHERE gpu_id = ?
			ORDER BY ts DESC
			LIMIT 1
		`, gpuID)
		sample, err := scanGPUSample(row)
		if err == nil {
			return sample, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
		// sql.ErrNoRows means no sample data yet - return nil, not an error
	}

	return nil, nil
}

// GetGPUSamples returns GPU samples for a time range.
// If day is empty, uses today's database. Otherwise opens the specific day file.
func (db *DB) GetGPUSamples(ctx context.Context, gpuID int64, startTS, endTS int64, day string) ([]GPUSampleMinute, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	var database *sql.DB
	if day == "" || day == time.Now().UTC().Format(dayLayout) {
		database = db.day
	} else {
		if err := db.rotateDayDBReadOnly(ctx, day); err != nil {
			return nil, err
		}
		database = db.day
	}

	if database == nil {
		return nil, fmt.Errorf("no daily database available")
	}

	rows, err := database.QueryContext(ctx, `
		SELECT ts, gpu_id, n, util_gpu_sum, util_gpu_max, util_mem_sum,
		       temp_max, power_mw_sum, vram_used_max,
		       sm_clock_max, mem_clock_max, power_limit_mw, throttle_or, ecc_errors
		FROM gpu_samples
		WHERE gpu_id = ? AND ts >= ? AND ts <= ?
		ORDER BY ts
	`, gpuID, startTS, endTS)
	if err != nil {
		return nil, fmt.Errorf("query GPU samples: %w", err)
	}
	defer rows.Close()

	var samples []GPUSampleMinute
	for rows.Next() {
		sample, err := scanGPUSample(rows)
		if err != nil {
			return nil, fmt.Errorf("scan GPU sample: %w", err)
		}
		samples = append(samples, *sample)
	}
	return samples, rows.Err()
}

// scanGPUSample scans a row into GPUSampleMinute with computed averages.
func scanGPUSample(scanner interface {
	Scan(...any) error
}) (*GPUSampleMinute, error) {
	var s GPUSampleMinute
	var utilGPUSum, utilGPUMax, utilMemSum, tempMax, powerMWSum sql.NullInt64
	var vramUsedMax, smClockMax, memClockMax, powerLimitMW, throttleOR, eccErrors sql.NullInt64

	if err := scanner.Scan(
		&s.TS, &s.GPUID, &s.N,
		&utilGPUSum, &utilGPUMax, &utilMemSum,
		&tempMax, &powerMWSum, &vramUsedMax,
		&smClockMax, &memClockMax, &powerLimitMW, &throttleOR, &eccErrors,
	); err != nil {
		return nil, err
	}

	if utilGPUSum.Valid && s.N > 0 {
		avg := utilGPUSum.Int64 / s.N
		s.UtilGPUAvg = &avg
	}
	if utilGPUMax.Valid {
		s.UtilGPUMax = &utilGPUMax.Int64
	}
	if utilMemSum.Valid && s.N > 0 {
		avg := utilMemSum.Int64 / s.N
		s.UtilMemAvg = &avg
	}
	if tempMax.Valid {
		s.TempMax = &tempMax.Int64
	}
	if powerMWSum.Valid && s.N > 0 {
		avg := powerMWSum.Int64 / s.N
		s.PowerMWAvg = &avg
	}
	if vramUsedMax.Valid {
		s.VRAMUsedMax = &vramUsedMax.Int64
	}
	if smClockMax.Valid {
		s.SMClockMax = &smClockMax.Int64
	}
	if memClockMax.Valid {
		s.MemClockMax = &memClockMax.Int64
	}
	if powerLimitMW.Valid {
		s.PowerLimitMW = &powerLimitMW.Int64
	}
	if throttleOR.Valid {
		s.ThrottleOR = &throttleOR.Int64
	}
	if eccErrors.Valid {
		s.ECCErrors = &eccErrors.Int64
	}

	return &s, nil
}