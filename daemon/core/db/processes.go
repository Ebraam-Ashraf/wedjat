package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Reasons a process stops being tracked. They match the end_reason column.
const (
	EndReboot   = "reboot"
	EndVanished = "vanished"
	EndExit     = "exit"
)

// EndProcess marks a process as finished. It reports how many rows it closed,
// so zero means the process was already closed or is unknown.
func (db *DB) EndProcess(ctx context.Context, procID, endTS int64, reason string) (int64, error) {
	if procID < 0 || endTS < 0 {
		return 0, errors.New("db: invalid process exit")
	}
	if !validEndReason(reason) {
		return 0, fmt.Errorf("db: invalid end reason %q", reason)
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	return endProcessTx(ctx, db.meta, processEnd{procID: procID, endTS: endTS, reason: reason})
}

func (db *DB) EndProcessStatus(ctx context.Context, procID, endTS int64, reason string, exitCode, termSignal *int) (int64, error) {
	if procID < 0 || endTS < 0 || !validEndReason(reason) {
		return 0, errors.New("db: invalid process exit")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}
	return endProcessTx(ctx, db.meta, processEnd{procID: procID, endTS: endTS, reason: reason, exitCode: exitCode, termSig: termSignal})
}

type execRower interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func endProcessTx(ctx context.Context, tx execRower, end processEnd) (int64, error) {
	result, err := tx.ExecContext(ctx,
		`UPDATE procs SET end_ts = ?, end_reason = ?, exit_code = ?, term_signal = ?
		 WHERE proc_id = ? AND end_ts IS NULL`,
		end.endTS, end.reason, end.exitCode, end.termSig, end.procID)
	if err != nil {
		return 0, fmt.Errorf("end process %d: %w", end.procID, err)
	}
	return result.RowsAffected()
}

// CloseProcessesNotSeen ends every open process of the current boot that is
// absent from seen. Without it a process row stays open forever, because NVML
// only reports what is alive and the daemon has no exit events of its own.
//
// Callers must only pass a complete picture of the running processes. A
// partial list would close processes that are merely missing from a failed
// poll.
func (db *DB) CloseProcessesNotSeen(ctx context.Context, bootID string, seen []int64, endTS int64) (int64, error) {
	if endTS < 0 {
		return 0, errors.New("db: negative end timestamp")
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	return closeProcessesNotSeenTx(ctx, db.meta, bootID, seen, endTS)
}

func closeProcessesNotSeenTx(ctx context.Context, tx execRower, bootID string, seen []int64, endTS int64) (int64, error) {
	present := make(map[int64]struct{}, len(seen))
	for _, procID := range seen {
		present[procID] = struct{}{}
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT proc_id FROM procs WHERE boot_id = ? AND end_ts IS NULL`, bootID)
	if err != nil {
		return 0, fmt.Errorf("list running processes: %w", err)
	}
	defer rows.Close()

	var stale []int64
	for rows.Next() {
		var procID int64
		if err := rows.Scan(&procID); err != nil {
			return 0, err
		}
		if _, ok := present[procID]; !ok {
			stale = append(stale, procID)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Close the cursor explicitly before executing updates on the same connection.
	// The deferred close is a safety net for early returns.
	rows.Close()

	if len(stale) == 0 {
		return 0, nil
	}

	statement := `UPDATE procs SET end_ts = ?, end_reason = ? WHERE proc_id = ? AND end_ts IS NULL`
	var closed int64
	for _, procID := range stale {
		result, err := tx.ExecContext(ctx, statement, endTS, EndVanished, procID)
		if err != nil {
			return closed, fmt.Errorf("close vanished process %d: %w", procID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return closed, err
		}
		closed += affected
	}
	return closed, nil
}

// CloseProcessesFromOtherBoots ends every process left open by an earlier boot.
// A PID is only unique within one boot, so those rows can never be matched
// against a live process again and would otherwise stay open forever.
func (db *DB) CloseProcessesFromOtherBoots(ctx context.Context, bootID string, endTS int64) (int64, error) {
	if endTS < 0 {
		return 0, errors.New("db: negative end timestamp")
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	result, err := db.meta.ExecContext(ctx,
		`UPDATE procs SET end_ts = ?, end_reason = ?
		 WHERE boot_id <> ? AND end_ts IS NULL`,
		endTS, EndReboot, bootID)
	if err != nil {
		return 0, fmt.Errorf("close processes from earlier boots: %w", err)
	}
	return result.RowsAffected()
}

// PreviousCleanShutdown reports whether the daemon that ran before this one
// recorded a clean exit. It is the crash detector: a run that ends without
// setting clean_shutdown leaves the stored flag at false.
//
// The stored boot ID identifies the machine boot, not the daemon run, so it
// cannot distinguish a restart after a crash. Only the presence of the flag
// matters: without it there is no earlier run to judge.
func (db *DB) PreviousCleanShutdown(ctx context.Context) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return false, err
	}

	var clean sql.NullString
	err := db.meta.QueryRowContext(ctx,
		`SELECT v FROM daemon_state WHERE k = 'clean_shutdown'`).Scan(&clean)
	if errors.Is(err, sql.ErrNoRows) {
		// No earlier run ever recorded a state, so nothing crashed.
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read previous shutdown state: %w", err)
	}
	if !clean.Valid {
		return true, nil
	}
	return clean.String == "1", nil
}

// HeartbeatAge reports how long ago the daemon last recorded a heartbeat. A
// negative result means the stored heartbeat is in the future, which happens
// when the wall clock moves backwards.
func (db *DB) HeartbeatAge(ctx context.Context) (time.Duration, error) {
	db.mu.Lock()
	if err := db.checkOpen(); err != nil {
		db.mu.Unlock()
		return 0, err
	}
	var stored string
	err := db.meta.QueryRowContext(ctx,
		`SELECT v FROM daemon_state WHERE k = 'heartbeat_ts'`).Scan(&stored)
	db.mu.Unlock()

	if err != nil {
		return 0, fmt.Errorf("read heartbeat: %w", err)
	}
	unix, err := parseUnix(stored)
	if err != nil {
		return 0, fmt.Errorf("malformed heartbeat_ts: %w", err)
	}
	return time.Since(time.Unix(unix, 0)), nil
}

func validEndReason(reason string) bool {
	switch reason {
	case "exit", "signal", EndReboot, EndVanished:
		return true
	default:
		return false
	}
}

// parseUnix reads a timestamp that was stored as text.
func parseUnix(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}
