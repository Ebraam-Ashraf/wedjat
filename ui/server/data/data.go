// Package data provides read-only access to the daemon's SQLite databases
// for serving historical telemetry, aggregates, and incidents via HTTP.
package data

import (
	"context"
	"fmt"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
)

// Data provides read-only access to the daemon databases.
type Data struct {
	db *db.DB
}

// New opens the databases in read-only mode.
func New(ctx context.Context, dataDir string) (*Data, error) {
	d, err := db.OpenDBReadOnly(ctx, dataDir)
	if err != nil {
		return nil, fmt.Errorf("open read-only DB: %w", err)
	}
	return &Data{db: d}, nil
}

// Close closes the database connections.
func (d *Data) Close() error {
	return d.db.Close()
}

// ============================================================================
// Response types matching Node.js API format exactly
// ============================================================================

// StatusResponse matches Node.js /api/status response
type StatusResponse struct {
	Connected     bool   `json:"connected"`
	HeartbeatAge  int64  `json:"heartbeat_age"`
	Warning       string `json:"warning,omitempty"`
	SocketPath    string `json:"socket_path"`
}

// GPUResponse matches Node.js /api/gpus response
type GPUResponse struct {
	ID             int64   `json:"id"`
	GpuDbID        int64   `json:"gpu_db_id"` // real DB primary key for history/aggregates queries
	UUID           string  `json:"uuid"`
	Index          int64   `json:"index"`
	Name           string  `json:"name"`
	PCIBusID       string  `json:"pci_bus_id"`
	VRAMTotalBytes int64   `json:"vram_total_bytes"`
	DriverVersion  string  `json:"driver_version"`
	FirstSeen      int64   `json:"first_seen"`
	LastSeen       int64   `json:"last_seen"`
}

// ProcessResponse matches Node.js /api/processes response
type ProcessResponse struct {
	ID             int64   `json:"id"`
	PID            int64   `json:"pid"`
	Command        string  `json:"command"`
	GPU            int64   `json:"gpu"`
	GPUUUID        string  `json:"gpu_uuid"`
	LastVRAMBytes  int64   `json:"last_vram_bytes"`
	PeakVRAMBytes  int64   `json:"peak_vram_bytes"`
	FirstSeen      int64   `json:"first_seen"`
	Running        bool    `json:"running"`
}

// HistoryResponse matches Node.js /api/history response
type HistoryResponse struct {
	TS               int64   `json:"ts"`
	Time             string  `json:"time"`
	GPUName          string  `json:"gpu_name"`
	GPUUUID          string  `json:"gpu_uuid"`
	N                int64   `json:"n"`
	UtilGPUAvg       float64 `json:"util_gpu_avg"`
	UtilGPUMax       float64 `json:"util_gpu_max"`
	TempMaxC         float64 `json:"temp_max_c"`
	VRAMUsedMaxBytes int64   `json:"vram_used_max_bytes"`
	PowerMWSum       int64   `json:"power_mw_sum"`
}

// AggregateResponse matches Node.js /api/aggregates response
type AggregateResponse struct {
	TS           int64  `json:"ts"`
	Time         string `json:"time"`
	Command      string `json:"command"`
	TGID         int64  `json:"tgid"`
	GPUName      string `json:"gpu_name"`
	Launches     int64  `json:"launches"`
	MemcpyCalls  int64  `json:"memcpy_calls"`
	MemcpyBytes  int64  `json:"memcpy_bytes"`
	AllocCalls   int64  `json:"alloc_calls"`
	AllocBytes   int64  `json:"alloc_bytes"`
	FreeBytes    int64  `json:"free_bytes"`
	SyncCalls    int64  `json:"sync_calls"`
	SyncUsSum    int64  `json:"sync_us_sum"`
	SyncUsMax    int64  `json:"sync_us_max"`
	IoctlCalls   int64  `json:"ioctl_calls"`
	UvmFaults    int64  `json:"uvm_faults"`
	UvmEvicts    int64  `json:"uvm_evicts"`
	Errors       int64  `json:"errors"`
}

// IncidentResponse matches Node.js /api/incidents response
type IncidentResponse struct {
	IncidentID  int64  `json:"incident_id"`
	Type        string `json:"type"`
	FirstTS     int64  `json:"first_ts"`
	LastTS      int64  `json:"last_ts"`
	Occurrences int64  `json:"occurrences"`
	Summary     string `json:"summary"`
	Detail      string `json:"detail"`
	Command     string `json:"command"`
	TGID        int64  `json:"tgid"`
	GPUName     string `json:"gpu_name"`
	FirstTime   string `json:"first_time"`
	LastTime    string `json:"last_time"`
}

// ============================================================================
// Data access methods
// ============================================================================

// GetStatus returns the daemon status
func (d *Data) GetStatus(ctx context.Context) (*StatusResponse, error) {
	// Get heartbeat from daemon_state
	var heartbeatTS int64
	err := d.db.QueryMeta(ctx, `SELECT v FROM daemon_state WHERE k = 'heartbeat_ts'`, &heartbeatTS)
	if err != nil {
		return &StatusResponse{
			Connected:    false,
			HeartbeatAge: 0,
			Warning:      "daemon not running or no heartbeat",
			SocketPath:   "/run/wedjatd/socket",
		}, nil
	}

	now := time.Now().Unix()
	heartbeatAge := now - heartbeatTS

	connected := heartbeatAge < 30 // consider connected if heartbeat < 30 seconds ago
	warning := ""
	if heartbeatAge >= 30 {
		warning = "daemon heartbeat stale"
	}

	return &StatusResponse{
		Connected:     connected,
		HeartbeatAge:  heartbeatAge,
		Warning:       warning,
		SocketPath:    "/run/wedjatd/socket",
	}, nil
}

// ListGPUsResponse returns all GPUs in Node.js API format
func (d *Data) ListGPUsResponse(ctx context.Context) ([]GPUResponse, error) {
	gpus, err := d.db.ListGPUs(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]GPUResponse, len(gpus))
	for i := range gpus {
		g := &gpus[i]
		// Fetch the real DB primary key for this GPU by UUID.
		dbID, _ := d.db.GetGPUDBIDByUUID(ctx, g.UUID)
		result[i] = GPUResponse{
			ID:             dbID, // real PK, used by history/aggregates
			GpuDbID:        dbID,
			UUID:           g.UUID,
			Index:          g.Index,
			Name:           g.Name,
			PCIBusID:       g.PCIBusID,
			VRAMTotalBytes: 0,
			DriverVersion:  g.DriverVersion,
			FirstSeen:      g.SeenAtUnix,
			LastSeen:       g.SeenAtUnix,
		}
		if g.VRAMTotalBytes != nil {
			result[i].VRAMTotalBytes = *g.VRAMTotalBytes
		}
	}
	return result, nil
}

// ListProcessesResponse returns processes in Node.js API format
func (d *Data) ListProcessesResponse(ctx context.Context, bootID string, allUsers bool, limit, offset int) ([]ProcessResponse, error) {
	procs, err := d.db.ListProcesses(ctx, bootID, allUsers, limit, offset)
	if err != nil {
		return nil, err
	}

	result := make([]ProcessResponse, 0, len(procs))
	for i := range procs {
		p := &procs[i]
		running := p.EndTS == nil

		var lastVRAM, peakVRAM int64
		var gpuID int64
		var gpuUUID string
		if len(p.VRAM) > 0 {
			v := p.VRAM[0]
			lastVRAM = v.VRAMBytes
			peakVRAM = v.VRAMBytes
			gpuID = v.GPUID

			// Fetch GPU UUID
			gpuIdent, err := d.db.GetGPUIdentity(ctx, v.GPUID)
			if err == nil && gpuIdent != nil {
				gpuUUID = gpuIdent.UUID
			}
		}

		result = append(result, ProcessResponse{
			ID:            p.ProcID,
			PID:           p.TGID,
			Command:       p.Command,
			GPU:           gpuID,
			GPUUUID:       gpuUUID,
			LastVRAMBytes: lastVRAM,
			PeakVRAMBytes: peakVRAM,
			FirstSeen:     p.FirstSeenUnix,
			Running:       running,
		})
	}
	return result, nil
}

// GetHistoryResponse returns GPU history in Node.js API format
func (d *Data) GetHistoryResponse(ctx context.Context, gpuID int64, startTS, endTS int64, day string) ([]HistoryResponse, error) {
	samples, err := d.db.GetGPUSamples(ctx, gpuID, startTS, endTS, day)
	if err != nil {
		return nil, err
	}

	// Fetch GPU identity for name and UUID
	gpuIdent, err := d.db.GetGPUIdentity(ctx, gpuID)
	if err != nil {
		return nil, err
	}
	gpuName := "Unknown"
	gpuUUID := ""
	if gpuIdent != nil {
		gpuName = gpuIdent.Name
		gpuUUID = gpuIdent.UUID
	}

	result := make([]HistoryResponse, len(samples))
	for i := range samples {
		s := &samples[i]
		result[i] = HistoryResponse{
			TS:               s.TS,
			Time:             time.Unix(s.TS, 0).UTC().Format(time.RFC3339),
			GPUName:          gpuName,
			GPUUUID:          gpuUUID,
			N:                s.N,
			UtilGPUAvg:       0,
			UtilGPUMax:       0,
			TempMaxC:         0,
			VRAMUsedMaxBytes: 0,
			PowerMWSum:       0,
		}
		if s.UtilGPUAvg != nil {
			result[i].UtilGPUAvg = float64(*s.UtilGPUAvg)
		}
		if s.UtilGPUMax != nil {
			result[i].UtilGPUMax = float64(*s.UtilGPUMax)
		}
		if s.TempMax != nil {
			result[i].TempMaxC = float64(*s.TempMax)
		}
		if s.VRAMUsedMax != nil {
			result[i].VRAMUsedMaxBytes = *s.VRAMUsedMax
		}
		if s.PowerMWAvg != nil {
			result[i].PowerMWSum = *s.PowerMWAvg * s.N
		}
	}
	return result, nil
}

// GetAggregatesResponse returns aggregates in Node.js API format
func (d *Data) GetAggregatesResponse(ctx context.Context, procID, gpuID int64, startTS, endTS int64, day string) ([]AggregateResponse, error) {
	aggs, err := d.db.GetAggregates(ctx, procID, gpuID, startTS, endTS, day)
	if err != nil {
		return nil, err
	}

	result := make([]AggregateResponse, len(aggs))
	for i := range aggs {
		a := &aggs[i]

		var command string
		var tgid int64
		var gpuName string

		if a.ProcessID > 0 {
			procIdent, err := d.db.GetProcessIdentity(ctx, a.ProcessID)
			if err == nil && procIdent != nil {
				command = procIdent.Command
				tgid = procIdent.TGID
			}
		}

		if a.GPUID > 0 {
			gpuIdent, err := d.db.GetGPUIdentity(ctx, a.GPUID)
			if err == nil && gpuIdent != nil {
				gpuName = gpuIdent.Name
			}
		}

		result[i] = AggregateResponse{
			TS:          a.TS,
			Time:        time.Unix(a.TS, 0).UTC().Format(time.RFC3339),
			Command:     command,
			TGID:        tgid,
			GPUName:     gpuName,
			Launches:    a.Launches,
			MemcpyCalls: a.MemcpyCalls,
			MemcpyBytes: a.MemcpyBytes,
			AllocCalls:  a.AllocCalls,
			AllocBytes:  a.AllocBytes,
			FreeBytes:   a.FreeBytes,
			SyncCalls:   a.SyncCalls,
			SyncUsSum:   a.SyncUsSum,
			SyncUsMax:   a.SyncUsMax,
			IoctlCalls:  a.IoctlCalls,
			UvmFaults:   a.UvmFaults,
			UvmEvicts:   a.UvmEvicts,
			Errors:      a.Errors,
		}
	}
	return result, nil
}

// ListIncidentsResponse returns incidents in Node.js API format
func (d *Data) ListIncidentsResponse(ctx context.Context, incidentType string, processID, gpuID int64, startTS, endTS int64, limit, offset int) ([]IncidentResponse, error) {
	incs, err := d.db.ListIncidents(ctx, incidentType, processID, gpuID, startTS, endTS, limit, offset)
	if err != nil {
		return nil, err
	}

	result := make([]IncidentResponse, len(incs))
	for i := range incs {
		inc := &incs[i]
		var command string
		var tgid int64
		var gpuName string

		if inc.Process != nil {
			command = inc.Process.Command
			tgid = inc.Process.TGID
		}
		if inc.GPU != nil {
			gpuName = inc.GPU.Name
		}

		result[i] = IncidentResponse{
			IncidentID:  inc.IncidentID,
			Type:        inc.Type,
			FirstTS:     inc.FirstTS,
			LastTS:      inc.LastTS,
			Occurrences: inc.Occurrences,
			Summary:     inc.Summary,
			Detail:      inc.Detail,
			Command:     command,
			TGID:        tgid,
			GPUName:     gpuName,
			FirstTime:   time.Unix(inc.FirstTS, 0).UTC().Format(time.RFC3339),
			LastTime:    time.Unix(inc.LastTS, 0).UTC().Format(time.RFC3339),
		}
	}
	return result, nil
}

// GetIncidentResponse returns a single incident in Node.js API format
func (d *Data) GetIncidentResponse(ctx context.Context, incidentID int64) (*IncidentResponse, error) {
	inc, err := d.db.GetIncident(ctx, incidentID)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return nil, nil
	}

	var command string
	var tgid int64
	var gpuName string

	if inc.Process != nil {
		command = inc.Process.Command
		tgid = inc.Process.TGID
	}
	if inc.GPU != nil {
		gpuName = inc.GPU.Name
	}

	return &IncidentResponse{
		IncidentID:  inc.IncidentID,
		Type:        inc.Type,
		FirstTS:     inc.FirstTS,
		LastTS:      inc.LastTS,
		Occurrences: inc.Occurrences,
		Summary:     inc.Summary,
		Detail:      inc.Detail,
		Command:     command,
		TGID:        tgid,
		GPUName:     gpuName,
		FirstTime:   time.Unix(inc.FirstTS, 0).UTC().Format(time.RFC3339),
		LastTime:    time.Unix(inc.LastTS, 0).UTC().Format(time.RFC3339),
	}, nil
}

// IncidentsByType returns incident counts grouped by type
func (d *Data) IncidentsByType(ctx context.Context, startTS, endTS int64) (map[string]int64, error) {
	return d.db.IncidentsByType(ctx, startTS, endTS)
}

// RecentIncidentsResponse returns the most recent incidents in Node.js API format
func (d *Data) RecentIncidentsResponse(ctx context.Context, limit int) ([]IncidentResponse, error) {
	incs, err := d.db.RecentIncidents(ctx, limit)
	if err != nil {
		return nil, err
	}

	result := make([]IncidentResponse, len(incs))
	for i := range incs {
		inc := &incs[i]
		var command string
		var tgid int64
		var gpuName string

		if inc.Process != nil {
			command = inc.Process.Command
			tgid = inc.Process.TGID
		}
		if inc.GPU != nil {
			gpuName = inc.GPU.Name
		}

		result[i] = IncidentResponse{
			IncidentID:  inc.IncidentID,
			Type:        inc.Type,
			FirstTS:     inc.FirstTS,
			LastTS:      inc.LastTS,
			Occurrences: inc.Occurrences,
			Summary:     inc.Summary,
			Detail:      inc.Detail,
			Command:     command,
			TGID:        tgid,
			GPUName:     gpuName,
			FirstTime:   time.Unix(inc.FirstTS, 0).UTC().Format(time.RFC3339),
			LastTime:    time.Unix(inc.LastTS, 0).UTC().Format(time.RFC3339),
		}
	}
	return result, nil
}

// GetGPUDBIDByUUID returns the internal database PK for a GPU identified by UUID.
// Returns 0 if not found.
func (d *Data) GetGPUDBIDByUUID(ctx context.Context, uuid string) (int64, error) {
	return d.db.GetGPUDBIDByUUID(ctx, uuid)
}

// DayLayout is the date format used for daily database files.
const DayLayout = "2006-01-02"

// Today returns today's date in the format used for daily database files.
func Today() string {
	return time.Now().UTC().Format(DayLayout)
}