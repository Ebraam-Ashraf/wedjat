package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AggregateMinute represents one minute of aggregated CUDA counters for a process on a GPU.
type AggregateMinute struct {
	TS         int64 `json:"ts"`
	ProcessID  int64 `json:"process_id"`
	GPUID      int64 `json:"gpu_id"`

	Launches    int64 `json:"launches"`
	MemcpyCalls int64 `json:"memcpy_calls"`
	MemcpyBytes int64 `json:"memcpy_bytes"`
	AllocCalls  int64 `json:"alloc_calls"`
	AllocBytes  int64 `json:"alloc_bytes"`
	FreeBytes   int64 `json:"free_bytes"`
	SyncCalls   int64 `json:"sync_calls"`
	SyncUsSum   int64 `json:"sync_us_sum"`
	SyncUsMax   int64 `json:"sync_us_max"`
	IoctlCalls  int64 `json:"ioctl_calls"`
	UvmFaults   int64 `json:"uvm_faults"`
	UvmEvicts   int64 `json:"uvm_evicts"`
	Errors      int64 `json:"errors"`
}

// GetAggregates returns aggregate data for a time range.
// If day is empty, uses today's database. Otherwise opens the specific day file.
func (db *DB) GetAggregates(ctx context.Context, processID, gpuID int64, startTS, endTS int64, day string) ([]AggregateMinute, error) {
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

	query := `
		SELECT ts, proc_id, gpu_id, launches, memcpy_calls, memcpy_bytes,
		       alloc_calls, alloc_bytes, free_bytes, sync_calls, sync_us_sum,
		       sync_us_max, ioctl_calls, uvm_faults, uvm_evicts, errors
		FROM agg
		WHERE 1=1
	`
	args := []any{}

	if processID > 0 {
		query += ` AND proc_id = ?`
		args = append(args, processID)
	}
	if gpuID > 0 {
		query += ` AND gpu_id = ?`
		args = append(args, gpuID)
	}
	if startTS > 0 {
		query += ` AND ts >= ?`
		args = append(args, startTS)
	}
	if endTS > 0 {
		query += ` AND ts <= ?`
		args = append(args, endTS)
	}
	query += ` ORDER BY ts`

	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query aggregates: %w", err)
	}
	defer rows.Close()

	var aggregates []AggregateMinute
	for rows.Next() {
		var a AggregateMinute
		if err := rows.Scan(
			&a.TS, &a.ProcessID, &a.GPUID,
			&a.Launches, &a.MemcpyCalls, &a.MemcpyBytes,
			&a.AllocCalls, &a.AllocBytes, &a.FreeBytes,
			&a.SyncCalls, &a.SyncUsSum, &a.SyncUsMax,
			&a.IoctlCalls, &a.UvmFaults, &a.UvmEvicts, &a.Errors,
		); err != nil {
			return nil, fmt.Errorf("scan aggregate: %w", err)
		}
		aggregates = append(aggregates, a)
	}
	return aggregates, rows.Err()
}

// GetAggregatesByProcess returns aggregate data for a specific process across all GPUs.
func (db *DB) GetAggregatesByProcess(ctx context.Context, processID int64, startTS, endTS int64, day string) ([]AggregateMinute, error) {
	return db.GetAggregates(ctx, processID, 0, startTS, endTS, day)
}

// GetAggregatesByGPU returns aggregate data for a specific GPU across all processes.
func (db *DB) GetAggregatesByGPU(ctx context.Context, gpuID int64, startTS, endTS int64, day string) ([]AggregateMinute, error) {
	return db.GetAggregates(ctx, 0, gpuID, startTS, endTS, day)
}

// AggregateSummary provides summary statistics for aggregates.
type AggregateSummary struct {
	ProcessID  int64 `json:"process_id"`
	GPUID      int64 `json:"gpu_id"`
	Minutes    int64 `json:"minutes"`

	TotalLaunches    int64 `json:"total_launches"`
	TotalMemcpyCalls int64 `json:"total_memcpy_calls"`
	TotalMemcpyBytes int64 `json:"total_memcpy_bytes"`
	TotalAllocCalls  int64 `json:"total_alloc_calls"`
	TotalAllocBytes  int64 `json:"total_alloc_bytes"`
	TotalFreeBytes   int64 `json:"total_free_bytes"`
	TotalSyncCalls   int64 `json:"total_sync_calls"`
	TotalSyncUsSum   int64 `json:"total_sync_us_sum"`
	MaxSyncUsMax     int64 `json:"max_sync_us_max"`
	TotalIoctlCalls  int64 `json:"total_ioctl_calls"`
	TotalUvmFaults   int64 `json:"total_uvm_faults"`
	TotalUvmEvicts   int64 `json:"total_uvm_evicts"`
	TotalErrors      int64 `json:"total_errors"`
}

// GetAggregateSummary returns summary statistics for aggregates in a time range.
func (db *DB) GetAggregateSummary(ctx context.Context, processID, gpuID int64, startTS, endTS int64, day string) (*AggregateSummary, error) {
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

	query := `
		SELECT COUNT(*) as minutes,
		       SUM(launches), SUM(memcpy_calls), SUM(memcpy_bytes),
		       SUM(alloc_calls), SUM(alloc_bytes), SUM(free_bytes),
		       SUM(sync_calls), SUM(sync_us_sum), MAX(sync_us_max),
		       SUM(ioctl_calls), SUM(uvm_faults), SUM(uvm_evicts), SUM(errors)
		FROM agg
		WHERE 1=1
	`
	args := []any{}

	if processID > 0 {
		query += ` AND proc_id = ?`
		args = append(args, processID)
	}
	if gpuID > 0 {
		query += ` AND gpu_id = ?`
		args = append(args, gpuID)
	}
	if startTS > 0 {
		query += ` AND ts >= ?`
		args = append(args, startTS)
	}
	if endTS > 0 {
		query += ` AND ts <= ?`
		args = append(args, endTS)
	}

	row := database.QueryRowContext(ctx, query, args...)
	var s AggregateSummary
	s.ProcessID = processID
	s.GPUID = gpuID

	var minutes sql.NullInt64
	if err := row.Scan(
		&minutes,
		&s.TotalLaunches, &s.TotalMemcpyCalls, &s.TotalMemcpyBytes,
		&s.TotalAllocCalls, &s.TotalAllocBytes, &s.TotalFreeBytes,
		&s.TotalSyncCalls, &s.TotalSyncUsSum, &s.MaxSyncUsMax,
		&s.TotalIoctlCalls, &s.TotalUvmFaults, &s.TotalUvmEvicts, &s.TotalErrors,
	); err != nil {
		if err == sql.ErrNoRows {
			return &s, nil
		}
		return nil, fmt.Errorf("scan aggregate summary: %w", err)
	}
	if minutes.Valid {
		s.Minutes = minutes.Int64
	}
	return &s, nil
}

// TopProcessesByGPU returns processes with the most activity on a GPU.
func (db *DB) TopProcessesByGPU(ctx context.Context, gpuID int64, startTS, endTS int64, day string, limit int) ([]AggregateSummary, error) {
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

	query := `
		SELECT proc_id, gpu_id,
		       COUNT(*) as minutes,
		       SUM(launches), SUM(memcpy_calls), SUM(memcpy_bytes),
		       SUM(alloc_calls), SUM(alloc_bytes), SUM(free_bytes),
		       SUM(sync_calls), SUM(sync_us_sum), MAX(sync_us_max),
		       SUM(ioctl_calls), SUM(uvm_faults), SUM(uvm_evicts), SUM(errors)
		FROM agg
		WHERE gpu_id = ?
	`
	args := []any{gpuID}

	if startTS > 0 {
		query += ` AND ts >= ?`
		args = append(args, startTS)
	}
	if endTS > 0 {
		query += ` AND ts <= ?`
		args = append(args, endTS)
	}
	query += ` GROUP BY proc_id, gpu_id`
	query += ` ORDER BY SUM(launches + memcpy_calls + alloc_calls + sync_calls) DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query top processes: %w", err)
	}
	defer rows.Close()

	var summaries []AggregateSummary
	for rows.Next() {
		var s AggregateSummary
		var minutes sql.NullInt64
		if err := rows.Scan(
			&s.ProcessID, &s.GPUID, &minutes,
			&s.TotalLaunches, &s.TotalMemcpyCalls, &s.TotalMemcpyBytes,
			&s.TotalAllocCalls, &s.TotalAllocBytes, &s.TotalFreeBytes,
			&s.TotalSyncCalls, &s.TotalSyncUsSum, &s.MaxSyncUsMax,
			&s.TotalIoctlCalls, &s.TotalUvmFaults, &s.TotalUvmEvicts, &s.TotalErrors,
		); err != nil {
			return nil, fmt.Errorf("scan top process: %w", err)
		}
		if minutes.Valid {
			s.Minutes = minutes.Int64
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}