package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Aggregate is one process on one GPU for one interval, already merged across
// every CUDA API the kernel counted. The agg table is keyed by
// (ts, proc_id, gpu_id), so the per-API rows the kernel keeps are combined
// here before they are written.
type Aggregate struct {
	ProcessID int64
	GPUID     int64

	Launches    int64
	MemcpyCalls int64
	MemcpyBytes int64
	AllocCalls  int64
	AllocBytes  int64
	FreeBytes   int64
	SyncCalls   int64
	SyncUsSum   int64
	SyncUsMax   int64
	IoctlCalls  int64
	UvmFaults   int64
	UvmEvicts   int64
	Errors      int64
}

// WriteAggregates folds one interval of kernel counters into the agg table.
// Repeated writes for the same minute add up and keep the largest latency.
func (db *DB) WriteAggregates(ctx context.Context, at time.Time, rows []Aggregate) error {
	if len(rows) == 0 {
		return nil
	}
	for i, row := range rows {
		if row.ProcessID < 0 || row.GPUID < 0 {
			return fmt.Errorf("aggregate %d has a negative identity", i)
		}
		for _, counter := range []int64{
			row.Launches, row.MemcpyCalls, row.MemcpyBytes, row.AllocCalls,
			row.AllocBytes, row.FreeBytes, row.SyncCalls, row.SyncUsSum,
			row.SyncUsMax, row.IoctlCalls, row.UvmFaults, row.UvmEvicts, row.Errors,
		} {
			if counter < 0 {
				return fmt.Errorf("aggregate %d has a negative counter", i)
			}
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

	if err := writeAggregatesTx(ctx, tx, minute, rows); err != nil {
		return err
	}
	return tx.Commit()
}

func writeAggregatesTx(ctx context.Context, tx *sql.Tx, minute int64, rows []Aggregate) error {
	stmt, err := tx.PrepareContext(ctx, aggregateUpsert)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx,
			minute, row.ProcessID, row.GPUID,
			row.Launches, row.MemcpyCalls, row.MemcpyBytes,
			row.AllocCalls, row.AllocBytes, row.FreeBytes,
			row.SyncCalls, row.SyncUsSum, row.SyncUsMax,
			row.IoctlCalls, row.UvmFaults, row.UvmEvicts, row.Errors,
		); err != nil {
			return fmt.Errorf("upsert aggregate: %w", err)
		}
	}
	return nil
}

// aggregateUpsert adds counters together and keeps the worst observed latency.
const aggregateUpsert = `INSERT INTO agg (
	ts, proc_id, gpu_id, launches, memcpy_calls, memcpy_bytes,
	alloc_calls, alloc_bytes, free_bytes, sync_calls, sync_us_sum,
	sync_us_max, ioctl_calls, uvm_faults, uvm_evicts, errors
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(ts, proc_id, gpu_id) DO UPDATE SET
	launches = agg.launches + excluded.launches,
	memcpy_calls = agg.memcpy_calls + excluded.memcpy_calls,
	memcpy_bytes = agg.memcpy_bytes + excluded.memcpy_bytes,
	alloc_calls = agg.alloc_calls + excluded.alloc_calls,
	alloc_bytes = agg.alloc_bytes + excluded.alloc_bytes,
	free_bytes = agg.free_bytes + excluded.free_bytes,
	sync_calls = agg.sync_calls + excluded.sync_calls,
	sync_us_sum = agg.sync_us_sum + excluded.sync_us_sum,
	sync_us_max = MAX(agg.sync_us_max, excluded.sync_us_max),
	ioctl_calls = agg.ioctl_calls + excluded.ioctl_calls,
	uvm_faults = agg.uvm_faults + excluded.uvm_faults,
	uvm_evicts = agg.uvm_evicts + excluded.uvm_evicts,
	errors = agg.errors + excluded.errors`

// Incident types.
const (
	IncidentSyncStall = "sync_stall"
	IncidentSyncHang  = "sync_hang"
	IncidentXid       = "xid"
)

// Incident is a notable event worth surfacing on its own.
type Incident struct {
	Type      string
	ProcessID *int64
	GPUID     *int64
	FirstTS   int64
	LastTS    int64
	DedupeKey string
	Summary   string
	Detail    string
}

// WriteIncident records an incident, extending an existing one when the same
// thing keeps happening within the dedupe window. It returns the incident id.
func (db *DB) WriteIncident(ctx context.Context, incident Incident) (int64, error) {
	if incident.Type == "" || incident.DedupeKey == "" {
		return 0, errors.New("db: incident type and dedupe key are required")
	}
	if len(incident.DedupeKey) > 512 {
		return 0, fmt.Errorf("db: incident dedupe key too long (%d bytes, max 512)", len(incident.DedupeKey))
	}
	if incident.FirstTS < 0 || incident.LastTS < 0 {
		return 0, errors.New("db: negative incident timestamp")
	}
	if incident.FirstTS > incident.LastTS {
		incident.FirstTS, incident.LastTS = incident.LastTS, incident.FirstTS
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	return db.writeIncidentTx(ctx, nil, incident)
}

func (db *DB) writeIncidentTx(ctx context.Context, tx *sql.Tx, incident Incident) (int64, error) {
	if incident.Type == "" || incident.DedupeKey == "" {
		return 0, errors.New("db: incident type and dedupe key are required")
	}
	if len(incident.DedupeKey) > 512 {
		return 0, fmt.Errorf("db: incident dedupe key too long (%d bytes, max 512)", len(incident.DedupeKey))
	}
	if incident.FirstTS < 0 || incident.LastTS < 0 {
		return 0, errors.New("db: negative incident timestamp")
	}
	if incident.FirstTS > incident.LastTS {
		incident.FirstTS, incident.LastTS = incident.LastTS, incident.FirstTS
	}
	// Fold into the existing row when one is still recent, so a repeating
	// problem is one incident with a rising occurrence count.
	//
	// The window is anchored on time.Now() rather than incident.LastTS so
	// that out-of-order or future-timestamped events cannot extend a closed
	// incident indefinitely, and a wall-clock jump cannot cause incidents
	// that should be distinct to collapse into one.
	const dedupeWindowSeconds = 60
	windowStart := time.Now().Unix() - dedupeWindowSeconds

	var incidentID int64
	query := func(query string, args ...any) error {
		if tx != nil {
			return tx.QueryRowContext(ctx, query, args...).Scan(&incidentID)
		}
		return db.meta.QueryRowContext(ctx, query, args...).Scan(&incidentID)
	}
	err := query(
		`UPDATE incidents
		    SET last_ts = MAX(last_ts, ?),
		        occurrences = occurrences + 1,
		        summary = COALESCE(NULLIF(?, ''), summary)
		  WHERE dedupe_key = ?
		    AND last_ts >= ?
		 RETURNING incident_id`,
		incident.LastTS, incident.Summary, incident.DedupeKey,
		windowStart)

	if errors.Is(err, sql.ErrNoRows) {
		if err := query(
			`INSERT INTO incidents
			   (type, proc_id, gpu_id, first_ts, last_ts, occurrences, dedupe_key, summary, detail)
			 VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)
			 RETURNING incident_id`,
			incident.Type, incident.ProcessID, incident.GPUID,
			incident.FirstTS, incident.LastTS,
			incident.DedupeKey, incident.Summary, incident.Detail); err != nil {
			return 0, fmt.Errorf("insert incident: %w", err)
		}
		return incidentID, nil
	}
	if err != nil {
		return 0, fmt.Errorf("update incident: %w", err)
	}
	return incidentID, nil
}
