package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
)

func TestReadOnlyDB(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// First, populate the DB using the real writer functions
	database, err := db.OpenDB(ctx, root)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	gpuID, err := database.UpsertGPU(ctx, db.GPUIdentity{
		UUID: "GPU-test", Index: 0, Name: "test GPU", SeenAtUnix: 100,
	})
	if err != nil {
		t.Fatalf("upsert GPU: %v", err)
	}
	t.Logf("GPU ID: %d", gpuID)

	procID, err := database.UpsertProcess(ctx, db.ProcessIdentity{
		BootID: "boot-test", TGID: 42, StartTicks: 7, Command: "test", FirstSeenUnix: 100,
	})
	if err != nil {
		t.Fatalf("upsert process: %v", err)
	}
	t.Logf("Process ID: %d", procID)

	// Use a timestamp from today so it matches the daily database file
	// that OpenDBReadOnly will try to open
	at := time.Now().UTC().Truncate(time.Minute)
	row := db.Aggregate{ProcessID: procID, GPUID: gpuID, Launches: 2, SyncUsSum: 10, SyncUsMax: 8}
	if err := database.WriteAggregates(ctx, at, []db.Aggregate{row}); err != nil {
		t.Fatalf("write aggregates: %v", err)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	// Debug: check what files exist
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		t.Logf("DB file: %s", e.Name())
	}

	// Direct query to meta.db to verify data - using same DSN as read-only DB
	metaPath := filepath.Join(root, "meta.db")
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_sync=NORMAL&_busy_timeout=5000&mode=ro", metaPath)
	metaDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open meta.db for debug: %v", err)
	}
	defer metaDB.Close()
	var count int
	err = metaDB.QueryRow("SELECT COUNT(*) FROM gpus").Scan(&count)
	if err != nil {
		t.Fatalf("query gpus count: %v", err)
	}
	t.Logf("Direct meta query - GPU count: %d", count)

	// Full SELECT query to see all columns
	rows, err := metaDB.Query("SELECT gpu_id, uuid, idx, name, pci_bus_id, vram_total_bytes, driver_version, first_seen_ts, last_seen_ts FROM gpus ORDER BY idx")
	if err != nil {
		t.Fatalf("query gpus full: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var gpuID, idx, vramTotal sql.NullInt64
		var uuid, name, pciBusID, driverVersion sql.NullString
		var firstSeen, lastSeen sql.NullInt64
		if err := rows.Scan(&gpuID, &uuid, &idx, &name, &pciBusID, &vramTotal, &driverVersion, &firstSeen, &lastSeen); err != nil {
			t.Fatalf("scan gpu: %v", err)
		}
		t.Logf("Direct scan: id=%v uuid=%v idx=%v name=%v pci=%v vram=%v driver=%v first=%v last=%v",
			gpuID, uuid, idx, name, pciBusID, vramTotal, driverVersion, firstSeen, lastSeen)
	}

	// Now open in read-only mode
	roDB, err := db.OpenDBReadOnly(ctx, root)
	if err != nil {
		t.Fatalf("open read-only database: %v", err)
	}
	defer roDB.Close()

	// Test ListGPUs
	t.Logf("About to call ListGPUs")
	gpus, err := roDB.ListGPUs(ctx)
	if err != nil {
		t.Fatalf("ListGPUs: %v", err)
	}
	t.Logf("GPUs found: %d", len(gpus))
	if len(gpus) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(gpus))
	}
	if gpus[0].UUID != "GPU-test" {
		t.Fatalf("unexpected GPU UUID: %s", gpus[0].UUID)
	}

	// Test ListProcesses
	processes, err := roDB.ListProcesses(ctx, "boot-test", false, 0, 0)
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(processes) != 1 {
		t.Fatalf("expected 1 process, got %d", len(processes))
	}
	if processes[0].TGID != 42 {
		t.Fatalf("unexpected process TGID: %d", processes[0].TGID)
	}

	// Test GetGPUSamples
	_, err = roDB.GetGPUSamples(ctx, gpuID, 0, time.Now().Unix(), "")
	if err != nil {
		t.Fatalf("GetGPUSamples: %v", err)
	}

	// Test GetAggregates
	aggregates, err := roDB.GetAggregates(ctx, procID, gpuID, 0, time.Now().Unix(), "")
	if err != nil {
		t.Fatalf("GetAggregates: %v", err)
	}
	if len(aggregates) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggregates))
	}
	if aggregates[0].Launches != 2 {
		t.Fatalf("unexpected launches: %d", aggregates[0].Launches)
	}

	// Test ListIncidents (should be empty since we didn't write any)
	incidents, err := roDB.ListIncidents(ctx, "", 0, 0, 0, 0, 10, 0)
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(incidents) != 0 {
		t.Fatalf("expected 0 incidents, got %d", len(incidents))
	}

	// Test IncidentCount
	var incCount int64
	incCount, err = roDB.IncidentCount(ctx, "", 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("IncidentCount: %v", err)
	}
	if incCount != 0 {
		t.Fatalf("expected 0 incidents, got %d", incCount)
	}

	// Test ProcessCount
	procCount, err := roDB.ProcessCount(ctx, "boot-test", false)
	if err != nil {
		t.Fatalf("ProcessCount: %v", err)
	}
	if procCount != 1 {
		t.Fatalf("expected 1 process, got %d", procCount)
	}
}

func TestReadOnlyDBWriteFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// First, populate the DB using the real writer functions
	database, err := db.OpenDB(ctx, root)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	database.Close()

	// Now open in read-only mode
	roDB, err := db.OpenDBReadOnly(ctx, root)
	if err != nil {
		t.Fatalf("open read-only database: %v", err)
	}
	defer roDB.Close()

	// Try to write - this should fail
	_, err = roDB.UpsertGPU(ctx, db.GPUIdentity{
		UUID: "GPU-readonly", Index: 1, Name: "should fail", SeenAtUnix: 200,
	})
	if err == nil {
		t.Fatal("expected write to read-only DB to fail, but it succeeded")
	}
}

func TestReadOnlyDBWithIncidents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Populate DB with writer functions
	database, err := db.OpenDB(ctx, root)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	gpuID, err := database.UpsertGPU(ctx, db.GPUIdentity{
		UUID: "GPU-inc", Index: 0, Name: "inc GPU", SeenAtUnix: 100,
	})
	if err != nil {
		t.Fatalf("upsert GPU: %v", err)
	}

	procID, err := database.UpsertProcess(ctx, db.ProcessIdentity{
		BootID: "boot-inc", TGID: 99, StartTicks: 10, Command: "inc-test", FirstSeenUnix: 100,
	})
	if err != nil {
		t.Fatalf("upsert process: %v", err)
	}

	now := time.Now().Unix()
	_, err = database.WriteIncident(ctx, db.Incident{
		Type: db.IncidentSyncStall, ProcessID: &procID, GPUID: &gpuID,
		FirstTS: now, LastTS: now,
		DedupeKey: "proc/gpu/sync/1", Summary: "slow sync",
	})
	if err != nil {
		t.Fatalf("write incident: %v", err)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	// Open read-only
	roDB, err := db.OpenDBReadOnly(ctx, root)
	if err != nil {
		t.Fatalf("open read-only database: %v", err)
	}
	defer roDB.Close()

	// Test ListIncidents
	incidents, err := roDB.ListIncidents(ctx, "", 0, 0, 0, 0, 10, 0)
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	if incidents[0].Summary != "slow sync" {
		t.Fatalf("unexpected summary: %s", incidents[0].Summary)
	}

	// Test GetIncident
	incident, err := roDB.GetIncident(ctx, incidents[0].IncidentID)
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if incident == nil {
		t.Fatal("GetIncident returned nil")
	}
	if incident.Summary != "slow sync" {
		t.Fatalf("unexpected summary: %s", incident.Summary)
	}

	// Test IncidentsByType
	byType, err := roDB.IncidentsByType(ctx, 0, 0)
	if err != nil {
		t.Fatalf("IncidentsByType: %v", err)
	}
	if len(byType) != 1 {
		t.Fatalf("expected 1 incident type, got %d", len(byType))
	}
	if byType[db.IncidentSyncStall] != 1 {
		t.Fatalf("unexpected count for sync_stall: %d", byType[db.IncidentSyncStall])
	}

	// Test RecentIncidents
	recent, err := roDB.RecentIncidents(ctx, 5)
	if err != nil {
		t.Fatalf("RecentIncidents: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent incident, got %d", len(recent))
	}
}
