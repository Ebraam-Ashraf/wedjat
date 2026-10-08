package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ProcessWithVRAM includes process identity with VRAM info.
type ProcessWithVRAM struct {
	ProcID int64
	ProcessIdentity
	EndTS      *int64        `json:"end_ts,omitempty"`
	EndReason  string        `json:"end_reason,omitempty"`
	ExitCode   *int64        `json:"exit_code,omitempty"`
	TermSignal *int64        `json:"term_signal,omitempty"`
	VRAM       []ProcessVRAM `json:"vram,omitempty"`
}

// ListProcesses returns processes, optionally including ended ones, newest first.
// bootID is no longer required – passing an empty string returns all processes.
func (db *DB) ListProcesses(ctx context.Context, bootID string, includeEnded bool, limit, offset int) ([]ProcessWithVRAM, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	query := `
		SELECT proc_id, boot_id, tgid, start_ticks, command, first_seen_ts,
		       end_ts, end_reason, exit_code, term_signal
		FROM procs
		WHERE 1=1
	`
	args := []any{}

	if bootID != "" {
		query += ` AND boot_id = ?`
		args = append(args, bootID)
	}
	if !includeEnded {
		query += ` AND end_ts IS NULL`
	}
	query += ` ORDER BY first_seen_ts DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
		if offset > 0 {
			query += ` OFFSET ?`
			args = append(args, offset)
		}
	}

	rows, err := db.meta.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	defer rows.Close()

	var processes []ProcessWithVRAM
	for rows.Next() {
		var p ProcessWithVRAM
		var endTS, exitCode, termSignal sql.NullInt64
		var endReason sql.NullString
		if err := rows.Scan(
			&p.ProcID, &p.BootID, &p.TGID, &p.StartTicks, &p.Command,
			&p.FirstSeenUnix, &endTS, &endReason, &exitCode, &termSignal,
		); err != nil {
			return nil, fmt.Errorf("scan process: %w", err)
		}
		if endTS.Valid {
			p.EndTS = &endTS.Int64
		}
		if endReason.Valid {
			p.EndReason = endReason.String
		}
		if exitCode.Valid {
			p.ExitCode = &exitCode.Int64
		}
		if termSignal.Valid {
			p.TermSignal = &termSignal.Int64
		}
		processes = append(processes, p)
	}

	// Fetch VRAM for each process
	for i := range processes {
		vram, err := db.processVRAM(ctx, processes[i].ProcID)
		if err != nil {
			return nil, err
		}
		processes[i].VRAM = vram
	}

	return processes, rows.Err()
}

// GetProcess returns a single process by ID with VRAM details.
func (db *DB) GetProcess(ctx context.Context, procID int64) (*ProcessWithVRAM, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	row := db.meta.QueryRowContext(ctx, `
		SELECT proc_id, boot_id, tgid, start_ticks, command, first_seen_ts,
		       end_ts, end_reason, exit_code, term_signal
		FROM procs
		WHERE proc_id = ?
	`, procID)

	var p ProcessWithVRAM
	var endTS, exitCode, termSignal sql.NullInt64
	var endReason sql.NullString
	if err := row.Scan(
		&p.ProcID, &p.BootID, &p.TGID, &p.StartTicks, &p.Command,
		&p.FirstSeenUnix, &endTS, &endReason, &exitCode, &termSignal,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get process: %w", err)
	}

	if endTS.Valid {
		p.EndTS = &endTS.Int64
	}
	if endReason.Valid {
		p.EndReason = endReason.String
	}
	if exitCode.Valid {
		p.ExitCode = &exitCode.Int64
	}
	if termSignal.Valid {
		p.TermSignal = &termSignal.Int64
	}

	vram, err := db.processVRAM(ctx, p.ProcID)
	if err != nil {
		return nil, err
	}
	p.VRAM = vram

	return &p, nil
}

// processVRAM fetches VRAM records for a process.
func (db *DB) processVRAM(ctx context.Context, procID int64) ([]ProcessVRAM, error) {
	rows, err := db.meta.QueryContext(ctx, `
		SELECT proc_id, gpu_id, first_seen_ts, last_seen_ts, peak_vram_bytes, last_vram_bytes
		FROM proc_gpu
		WHERE proc_id = ?
		ORDER BY gpu_id
	`, procID)
	if err != nil {
		return nil, fmt.Errorf("query process VRAM: %w", err)
	}
	defer rows.Close()

	var vram []ProcessVRAM
	for rows.Next() {
		var v ProcessVRAM
		var peakVRAM, lastVRAM sql.NullInt64
		if err := rows.Scan(&v.ProcessID, &v.GPUID, &v.SeenAtUnix, &v.SeenAtUnix, &peakVRAM, &lastVRAM); err != nil {
			return nil, fmt.Errorf("scan VRAM: %w", err)
		}
		if peakVRAM.Valid {
			v.VRAMBytes = peakVRAM.Int64 // Use peak as the reported value
		} else if lastVRAM.Valid {
			v.VRAMBytes = lastVRAM.Int64
		}
		vram = append(vram, v)
	}
	return vram, rows.Err()
}

// GetProcessesByGPU returns processes that used a specific GPU.
func (db *DB) GetProcessesByGPU(ctx context.Context, gpuID int64, startTS, endTS int64) ([]ProcessWithVRAM, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	query := `
		SELECT p.proc_id, p.boot_id, p.tgid, p.start_ticks, p.command, p.first_seen_ts,
		       p.end_ts, p.end_reason, p.exit_code, p.term_signal
		FROM procs p
		JOIN proc_gpu pg ON p.proc_id = pg.proc_id
		WHERE pg.gpu_id = ?
	`
	args := []any{gpuID}
	if startTS > 0 {
		query += ` AND p.first_seen_ts >= ?`
		args = append(args, startTS)
	}
	if endTS > 0 {
		query += ` AND (p.end_ts IS NULL OR p.end_ts <= ?)`
		args = append(args, endTS)
	}
	query += ` ORDER BY p.first_seen_ts DESC`

	rows, err := db.meta.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list processes by GPU: %w", err)
	}
	defer rows.Close()

	var processes []ProcessWithVRAM
	for rows.Next() {
		var p ProcessWithVRAM
		var endTS, exitCode, termSignal sql.NullInt64
		var endReason sql.NullString
		if err := rows.Scan(
			&p.ProcID, &p.BootID, &p.TGID, &p.StartTicks, &p.Command,
			&p.FirstSeenUnix, &endTS, &endReason, &exitCode, &termSignal,
		); err != nil {
			return nil, fmt.Errorf("scan process: %w", err)
		}
		if endTS.Valid {
			p.EndTS = &endTS.Int64
		}
		if endReason.Valid {
			p.EndReason = endReason.String
		}
		if exitCode.Valid {
			p.ExitCode = &exitCode.Int64
		}
		if termSignal.Valid {
			p.TermSignal = &termSignal.Int64
		}
		processes = append(processes, p)
	}

	return processes, rows.Err()
}

// RunningProcesses returns currently running processes (end_ts IS NULL).
func (db *DB) RunningProcesses(ctx context.Context, bootID string) ([]ProcessWithVRAM, error) {
	return db.ListProcesses(ctx, bootID, false, 0, 0)
}

// ProcessCount returns the count of processes, optionally filtered by bootID.
func (db *DB) ProcessCount(ctx context.Context, bootID string, includeEnded bool) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	query := `SELECT COUNT(*) FROM procs WHERE 1=1`
	args := []any{}
	if bootID != "" {
		query += ` AND boot_id = ?`
		args = append(args, bootID)
	}
	if !includeEnded {
		query += ` AND end_ts IS NULL`
	}

	var count int64
	err := db.meta.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}
