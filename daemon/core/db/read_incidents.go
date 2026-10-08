package db

import (
	"context"
	"database/sql"
	"fmt"
)

// IncidentWithDetails includes incident with joined process/GPU info.
type IncidentWithDetails struct {
	IncidentID int64
	Incident
	Occurrences int64            `json:"occurrences"`
	Process     *ProcessIdentity `json:"process,omitempty"`
	GPU         *GPUIdentity     `json:"gpu,omitempty"`
}

// ListIncidents returns incidents with optional filters.
func (db *DB) ListIncidents(ctx context.Context, incidentType string, processID, gpuID int64, startTS, endTS int64, limit, offset int) ([]IncidentWithDetails, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	query := `
		SELECT i.incident_id, i.type, i.proc_id, i.gpu_id, i.first_ts, i.last_ts,
		       i.occurrences, i.dedupe_key, i.summary, i.detail
		FROM incidents i
		WHERE 1=1
	`
	args := []any{}

	if incidentType != "" {
		query += ` AND i.type = ?`
		args = append(args, incidentType)
	}
	if processID > 0 {
		query += ` AND i.proc_id = ?`
		args = append(args, processID)
	}
	if gpuID > 0 {
		query += ` AND i.gpu_id = ?`
		args = append(args, gpuID)
	}
	if startTS > 0 {
		query += ` AND i.last_ts >= ?`
		args = append(args, startTS)
	}
	if endTS > 0 {
		query += ` AND i.first_ts <= ?`
		args = append(args, endTS)
	}
	query += ` ORDER BY i.last_ts DESC`
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
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()

	var incidents []IncidentWithDetails
	for rows.Next() {
		var inc IncidentWithDetails
		var procID, gpuID sql.NullInt64
		if err := rows.Scan(
			&inc.IncidentID, &inc.Type, &procID, &gpuID,
			&inc.FirstTS, &inc.LastTS, &inc.Occurrences,
			&inc.DedupeKey, &inc.Summary, &inc.Detail,
		); err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		if procID.Valid {
			inc.ProcessID = &procID.Int64
		}
		if gpuID.Valid {
			inc.GPUID = &gpuID.Int64
		}

		// Fetch process details if available
		if inc.ProcessID != nil {
			proc, err := db.getProcessIdentity(ctx, *inc.ProcessID)
			if err != nil {
				return nil, err
			}
			if proc != nil {
				inc.Process = proc
			}
		}

		// Fetch GPU details if available
		if inc.GPUID != nil {
			gpu, err := db.getGPUIdentity(ctx, *inc.GPUID)
			if err != nil {
				return nil, err
			}
			if gpu != nil {
				inc.GPU = gpu
			}
		}

		incidents = append(incidents, inc)
	}
	return incidents, rows.Err()
}

// GetIncident returns a single incident by ID with details.
func (db *DB) GetIncident(ctx context.Context, incidentID int64) (*IncidentWithDetails, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	row := db.meta.QueryRowContext(ctx, `
		SELECT incident_id, type, proc_id, gpu_id, first_ts, last_ts,
		       occurrences, dedupe_key, summary, detail
		FROM incidents
		WHERE incident_id = ?
	`, incidentID)

	var inc IncidentWithDetails
	var procID, gpuID sql.NullInt64
	if err := row.Scan(
		&inc.IncidentID, &inc.Type, &procID, &gpuID,
		&inc.FirstTS, &inc.LastTS, &inc.Occurrences,
		&inc.DedupeKey, &inc.Summary, &inc.Detail,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	if procID.Valid {
		inc.ProcessID = &procID.Int64
	}
	if gpuID.Valid {
		inc.GPUID = &gpuID.Int64
	}

	if inc.ProcessID != nil {
		proc, err := db.getProcessIdentity(ctx, *inc.ProcessID)
		if err != nil {
			return nil, err
		}
		if proc != nil {
			inc.Process = proc
		}
	}
	if inc.GPUID != nil {
		gpu, err := db.getGPUIdentity(ctx, *inc.GPUID)
		if err != nil {
			return nil, err
		}
		if gpu != nil {
			inc.GPU = gpu
		}
	}

	return &inc, nil
}

// getProcessIdentity fetches process identity by ID.
func (db *DB) getProcessIdentity(ctx context.Context, procID int64) (*ProcessIdentity, error) {
	row := db.meta.QueryRowContext(ctx, `
		SELECT boot_id, tgid, start_ticks, command, first_seen_ts
		FROM procs
		WHERE proc_id = ?
	`, procID)

	var p ProcessIdentity
	if err := row.Scan(&p.BootID, &p.TGID, &p.StartTicks, &p.Command, &p.FirstSeenUnix); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get process identity: %w", err)
	}
	return &p, nil
}

// getGPUIdentity fetches GPU identity by ID.
func (db *DB) getGPUIdentity(ctx context.Context, gpuID int64) (*GPUIdentity, error) {
	row := db.meta.QueryRowContext(ctx, `
		SELECT uuid, idx, name, pci_bus_id, vram_total_bytes, driver_version, first_seen_ts, last_seen_ts
		FROM gpus
		WHERE gpu_id = ?
	`, gpuID)

	var g GPUIdentity
	var vramTotal sql.NullInt64
	var pciBusID, driverVersion sql.NullString
	if err := row.Scan(&g.UUID, &g.Index, &g.Name, &pciBusID, &vramTotal, &driverVersion, &g.SeenAtUnix, &g.SeenAtUnix); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get GPU identity: %w", err)
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
	return &g, nil
}

// IncidentCount returns the count of incidents matching filters.
func (db *DB) IncidentCount(ctx context.Context, incidentType string, processID, gpuID int64, startTS, endTS int64) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return 0, err
	}

	query := `SELECT COUNT(*) FROM incidents WHERE 1=1`
	args := []any{}

	if incidentType != "" {
		query += ` AND type = ?`
		args = append(args, incidentType)
	}
	if processID > 0 {
		query += ` AND proc_id = ?`
		args = append(args, processID)
	}
	if gpuID > 0 {
		query += ` AND gpu_id = ?`
		args = append(args, gpuID)
	}
	if startTS > 0 {
		query += ` AND last_ts >= ?`
		args = append(args, startTS)
	}
	if endTS > 0 {
		query += ` AND first_ts <= ?`
		args = append(args, endTS)
	}

	var count int64
	err := db.meta.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

// IncidentsByType returns incident counts grouped by type.
func (db *DB) IncidentsByType(ctx context.Context, startTS, endTS int64) (map[string]int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	query := `SELECT type, COUNT(*) FROM incidents WHERE 1=1`
	args := []any{}

	if startTS > 0 {
		query += ` AND last_ts >= ?`
		args = append(args, startTS)
	}
	if endTS > 0 {
		query += ` AND first_ts <= ?`
		args = append(args, endTS)
	}
	query += ` GROUP BY type ORDER BY COUNT(*) DESC`

	rows, err := db.meta.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query incidents by type: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var incidentType string
		var count int64
		if err := rows.Scan(&incidentType, &count); err != nil {
			return nil, fmt.Errorf("scan incident type: %w", err)
		}
		result[incidentType] = count
	}
	return result, rows.Err()
}

// RecentIncidents returns the most recent incidents across all types.
func (db *DB) RecentIncidents(ctx context.Context, limit int) ([]IncidentWithDetails, error) {
	return db.ListIncidents(ctx, "", 0, 0, 0, 0, limit, 0)
}
