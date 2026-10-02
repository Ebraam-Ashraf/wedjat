package db_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
	_ "modernc.org/sqlite"
)

func TestPersistenceAndIntervalMerges(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
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
	if _, err := database.UpsertGPU(ctx, db.GPUIdentity{
		UUID: "GPU-test", Index: 0, Name: "updated name", SeenAtUnix: 200,
	}); err != nil {
		t.Fatalf("upsert GPU again: %v", err)
	}

	procID, err := database.UpsertProcess(ctx, db.ProcessIdentity{
		BootID: "boot-test", TGID: 42, StartTicks: 7, Command: "test", FirstSeenUnix: 100,
	})
	if err != nil {
		t.Fatalf("upsert process: %v", err)
	}

	at := time.Unix(1_700_000_123, 0).UTC()
	row := db.Aggregate{ProcessID: procID, GPUID: gpuID, Launches: 2, SyncUsSum: 10, SyncUsMax: 8}
	if err := database.WriteAggregates(ctx, at, []db.Aggregate{row}); err != nil {
		t.Fatalf("write aggregates: %v", err)
	}
	row.Launches = 3
	row.SyncUsSum = 4
	row.SyncUsMax = 12
	if err := database.WriteAggregates(ctx, at.Add(20*time.Second), []db.Aggregate{row}); err != nil {
		t.Fatalf("write aggregates again: %v", err)
	}

	firstID, err := database.WriteIncident(ctx, db.Incident{
		Type: db.IncidentSyncStall, ProcessID: &procID, GPUID: &gpuID,
		FirstTS: time.Now().Unix(), LastTS: time.Now().Unix(),
		DedupeKey: "proc/gpu/sync", Summary: "slow",
	})
	if err != nil {
		t.Fatalf("write incident: %v", err)
	}
	secondID, err := database.WriteIncident(ctx, db.Incident{
		Type: db.IncidentSyncStall, DedupeKey: "proc/gpu/sync",
		FirstTS: time.Now().Unix(), LastTS: time.Now().Unix(),
		Summary: "still slow",
	})
	if err != nil {
		t.Fatalf("dedupe incident: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("dedupe created incident %d, want existing %d", secondID, firstID)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	meta := openTestSQLite(t, filepath.Join(root, "meta.db"))
	defer meta.Close()
	var firstSeen int64
	if err := meta.QueryRow("SELECT first_seen_ts FROM gpus WHERE gpu_id = ?", gpuID).Scan(&firstSeen); err != nil {
		t.Fatalf("read GPU metadata: %v", err)
	}
	var occurrences int64
	if err := meta.QueryRow("SELECT occurrences FROM incidents WHERE incident_id = ?", firstID).Scan(&occurrences); err != nil {
		t.Fatalf("read incident metadata: %v", err)
	}
	if firstSeen != 100 || occurrences != 2 {
		t.Fatalf("metadata = first_seen %d, occurrences %d; want 100, 2", firstSeen, occurrences)
	}

	day := openTestSQLite(t, filepath.Join(root, at.Format("2006-01-02")+".db"))
	defer day.Close()
	var launches, syncSum, syncMax int64
	if err := day.QueryRow("SELECT launches, sync_us_sum, sync_us_max FROM agg").Scan(&launches, &syncSum, &syncMax); err != nil {
		t.Fatalf("read aggregate: %v", err)
	}
	if launches != 5 || syncSum != 14 || syncMax != 12 {
		t.Fatalf("aggregate = launches %d, sum %d, max %d; want 5, 14, 12", launches, syncSum, syncMax)
	}
}

func TestInputValidation(t *testing.T) {
	database, err := db.OpenDB(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	if _, err := database.UpsertGPU(context.Background(), db.GPUIdentity{}); err == nil {
		t.Fatal("expected empty GPU UUID to be rejected")
	}
	if err := database.WriteAggregates(context.Background(), time.Now(), []db.Aggregate{{ProcessID: -1}}); err == nil {
		t.Fatal("expected negative aggregate identity to be rejected")
	}
	if _, err := database.WriteIncident(context.Background(), db.Incident{Type: "x"}); err == nil {
		t.Fatal("expected incomplete incident to be rejected")
	}
}

// TestCrossDayAggregateSplit verifies that two WriteAggregates calls whose
// timestamps fall on different UTC days each write into their own daily .db
// file. No data from day A must appear in day B's file and vice versa.
func TestCrossDayAggregateSplit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database, err := db.OpenDB(ctx, root)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	gpuID, err := database.UpsertGPU(ctx, db.GPUIdentity{UUID: "GPU-split", Index: 0, Name: "split GPU", SeenAtUnix: 1})
	if err != nil {
		t.Fatalf("upsert GPU: %v", err)
	}
	procID, err := database.UpsertProcess(ctx, db.ProcessIdentity{
		BootID: "boot-split", TGID: 1, StartTicks: 1, Command: "split", FirstSeenUnix: 1,
	})
	if err != nil {
		t.Fatalf("upsert process: %v", err)
	}

	// Two timestamps on consecutive UTC days.
	day1 := time.Date(2025, 3, 14, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2025, 3, 15, 0, 1, 0, 0, time.UTC)

	row1 := db.Aggregate{ProcessID: procID, GPUID: gpuID, Launches: 7, SyncUsSum: 100, SyncUsMax: 50}
	if err := database.WriteAggregates(ctx, day1, []db.Aggregate{row1}); err != nil {
		t.Fatalf("write day1 aggregates: %v", err)
	}
	row2 := db.Aggregate{ProcessID: procID, GPUID: gpuID, Launches: 3, SyncUsSum: 40, SyncUsMax: 20}
	if err := database.WriteAggregates(ctx, day2, []db.Aggregate{row2}); err != nil {
		t.Fatalf("write day2 aggregates: %v", err)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	// Day 1 file must contain only the day-1 row.
	db1 := openTestSQLite(t, filepath.Join(root, day1.Format("2006-01-02")+".db"))
	defer db1.Close()
	var launches1 int64
	if err := db1.QueryRow("SELECT launches FROM agg").Scan(&launches1); err != nil {
		t.Fatalf("read day1 aggregate: %v", err)
	}
	if launches1 != 7 {
		t.Fatalf("day1 launches = %d, want 7", launches1)
	}

	// Day 2 file must contain only the day-2 row.
	db2 := openTestSQLite(t, filepath.Join(root, day2.Format("2006-01-02")+".db"))
	defer db2.Close()
	var launches2 int64
	if err := db2.QueryRow("SELECT launches FROM agg").Scan(&launches2); err != nil {
		t.Fatalf("read day2 aggregate: %v", err)
	}
	if launches2 != 3 {
		t.Fatalf("day2 launches = %d, want 3", launches2)
	}
}

// TestPruneDayFiles creates a mix of old and recent daily database files, then
// calls PruneDayFiles and verifies that only the files older than the retention
// window were removed.
func TestPruneDayFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database, err := db.OpenDB(ctx, root)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	now := time.Date(2025, 6, 10, 12, 0, 0, 0, time.UTC)
	const keepDays = 7

	// Create stub day files: 3 old, 2 recent, and 1 at the 7-day mark.
	// The cutoff is now - keepDays (a full timestamp at 12:00 UTC here), so a
	// file whose name is exactly 7 days ago parses as midnight and is strictly
	// before the cutoff — it is pruned, not kept.
	type dayCase struct {
		daysAgo int
		kept    bool
	}
	cases := []dayCase{
		{daysAgo: 30, kept: false},
		{daysAgo: 20, kept: false},
		{daysAgo: 8, kept: false},
		{daysAgo: 7, kept: false}, // midnight on cutoff day < cutoff at 12:00 — pruned
		{daysAgo: 3, kept: true},
		{daysAgo: 1, kept: true},
	}

	for _, c := range cases {
		date := now.AddDate(0, 0, -c.daysAgo).Format("2006-01-02")
		path := filepath.Join(root, date+".db")
		if err := os.WriteFile(path, []byte{}, 0644); err != nil {
			t.Fatalf("create stub day file %s: %v", path, err)
		}
	}

	removed, err := database.PruneDayFiles(ctx, keepDays, now)
	if err != nil {
		t.Fatalf("prune day files: %v", err)
	}
	if removed != 4 {
		t.Fatalf("removed %d files, want 4", removed)
	}

	for _, c := range cases {
		date := now.AddDate(0, 0, -c.daysAgo).Format("2006-01-02")
		path := filepath.Join(root, date+".db")
		_, statErr := os.Stat(path)
		exists := statErr == nil
		if exists != c.kept {
			if c.kept {
				t.Errorf("file %s was deleted but should have been kept", date+".db")
			} else {
				t.Errorf("file %s still exists but should have been pruned", date+".db")
			}
		}
	}
}

// TestHeartbeatAndShutdownRoundTrip verifies the daemon_state key/value
// persistence for the two most critical daemon lifecycle values:
//
//   - WriteHeartbeat / HeartbeatAge — the watchdog signal.
//   - WriteCleanShutdown / PreviousCleanShutdown — the crash detector.
func TestHeartbeatAndShutdownRoundTrip(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenDB(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	// Before any heartbeat is written, HeartbeatAge should return an error
	// because the key does not exist yet.
	if _, err := database.HeartbeatAge(ctx); err == nil {
		t.Fatal("HeartbeatAge before first write: expected error, got nil")
	}

	// Before any WriteCleanShutdown, PreviousCleanShutdown should return true
	// (no earlier run — nothing to call a crash).
	clean, err := database.PreviousCleanShutdown(ctx)
	if err != nil {
		t.Fatalf("PreviousCleanShutdown before first write: %v", err)
	}
	if !clean {
		t.Fatal("PreviousCleanShutdown before first write: expected true (no prior run), got false")
	}

	// Write a heartbeat and verify HeartbeatAge is small (< 5 s).
	if err := database.WriteHeartbeat(ctx); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	age, err := database.HeartbeatAge(ctx)
	if err != nil {
		t.Fatalf("HeartbeatAge after write: %v", err)
	}
	if age < 0 || age > 5*time.Second {
		t.Fatalf("HeartbeatAge = %v, want 0..5s", age)
	}

	// Mark an unclean shutdown; PreviousCleanShutdown must reflect that.
	if err := database.WriteCleanShutdown(ctx, false); err != nil {
		t.Fatalf("WriteCleanShutdown(false): %v", err)
	}
	clean, err = database.PreviousCleanShutdown(ctx)
	if err != nil {
		t.Fatalf("PreviousCleanShutdown after unclean write: %v", err)
	}
	if clean {
		t.Fatal("PreviousCleanShutdown: expected false after WriteCleanShutdown(false), got true")
	}

	// Overwrite with a clean shutdown; flag must flip.
	if err := database.WriteCleanShutdown(ctx, true); err != nil {
		t.Fatalf("WriteCleanShutdown(true): %v", err)
	}
	clean, err = database.PreviousCleanShutdown(ctx)
	if err != nil {
		t.Fatalf("PreviousCleanShutdown after clean write: %v", err)
	}
	if !clean {
		t.Fatal("PreviousCleanShutdown: expected true after WriteCleanShutdown(true), got false")
	}
}

func openTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatalf("ping %s: %v", path, err)
	}
	return database
}
