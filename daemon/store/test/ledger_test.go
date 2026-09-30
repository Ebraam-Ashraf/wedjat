package store_test

import (
	"context"
	"strconv"
	"testing"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/store"
	"time"
)

func TestLedgerIdentityUpsertsAndFixedOrdinalMapping(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s, err := Open(ctx, testOpenOptions(root, "boot-a", now, true))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	metaDB := openTestDB(t, s.MetaPath(), true)

	index := int64(3)
	total := int64(8 << 30)
	gpuID, err := s.UpsertGPU(ctx, GPUIdentity{
		UUID: "GPU-test", Index: &index, Name: "test GPU", PCIBusID: "0000:01:00.0",
		VRAMTotalBytes: &total, DriverVersion: "test-driver", SeenAt: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpsertGPU(ctx, GPUIdentity{
		UUID: "GPU-test", Name: "updated name", SeenAt: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated != gpuID {
		t.Fatalf("GPU upsert changed ID %d to %d", gpuID, updated)
	}
	var firstSeen, lastSeen int64
	var name string
	if err := metaDB.QueryRowContext(ctx, "SELECT first_seen_ts,last_seen_ts,name FROM gpus WHERE gpu_id=?", gpuID).
		Scan(&firstSeen, &lastSeen, &name); err != nil {
		t.Fatal(err)
	}
	if firstSeen != 100 || lastSeen != 200 || name != "updated name" {
		t.Fatalf("GPU metadata = %d %d %q", firstSeen, lastSeen, name)
	}

	proc := ProcessIdentity{
		BootID: "boot-a", TGID: 4321, StartTicks: 765, Command: "job",
		Cmdline: "job --run", FirstSeenAt: 300,
	}
	procID, err := s.UpsertProcess(ctx, proc)
	if err != nil {
		t.Fatal(err)
	}
	proc.Command = ""
	proc.Cmdline = ""
	proc.Container = "container-1"
	proc.FirstSeenAt = 500
	again, err := s.UpsertProcess(ctx, proc)
	if err != nil {
		t.Fatal(err)
	}
	if procID != again {
		t.Fatalf("process identity upsert changed ID %d to %d", procID, again)
	}
	if err := s.MapProcessDevice(ctx, procID, 0, gpuID); err != nil {
		t.Fatal(err)
	}
	if err := s.MapProcessDevice(ctx, procID, 0, gpuID); err != nil {
		t.Fatalf("idempotent ordinal mapping failed: %v", err)
	}
	if err := s.MapProcessDevice(ctx, procID, 0, 0); err == nil {
		t.Fatal("ordinal was silently remapped to a different GPU")
	}
}

func TestProcessGPUFlushIsAtomicWithHeartbeat(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s, err := Open(ctx, testOpenOptions(root, "boot-a", now, true))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	metaDB := openTestDB(t, s.MetaPath(), true)
	gpuID, err := s.UpsertGPU(ctx, GPUIdentity{UUID: "GPU-test", SeenAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	procID, err := s.UpsertProcess(ctx, ProcessIdentity{
		BootID: "boot-a", TGID: 222, StartTicks: 17, FirstSeenAt: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	rows := []ProcessGPUFlush{{
		ProcessID: procID, GPUID: gpuID, FirstSeenAt: 100, LastSeenAt: 200,
		PeakVRAM: int64ptr(4096), LastVRAM: int64ptr(2048),
		Launches: 3, MemcpyBytes: 4096, AllocBytes: 8192,
		FreeBytes: 1024, SyncCalls: 2, WorstSyncUS: 125, Errors: 1,
	}, {
		ProcessID: 99999, GPUID: gpuID, FirstSeenAt: 100, LastSeenAt: 100,
	}}
	if err := s.FlushProcessGPU(ctx, rows, now.Add(time.Minute)); err == nil {
		t.Fatal("invalid FK row did not fail the ledger transaction")
	}
	var count int
	if err := metaDB.QueryRowContext(ctx, "SELECT count(*) FROM proc_gpu").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed flush partially inserted %d rows", count)
	}
	var heartbeat string
	if err := metaDB.QueryRowContext(ctx, "SELECT v FROM daemon_state WHERE k='heartbeat_ts'").Scan(&heartbeat); err != nil {
		t.Fatal(err)
	}
	if heartbeat != strconv.FormatInt(now.Unix(), 10) {
		t.Fatalf("failed flush changed heartbeat to %s", heartbeat)
	}

	rows = rows[:1]
	if err := s.FlushProcessGPU(ctx, rows, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows[0].FirstSeenAt = 150
	rows[0].LastSeenAt = 300
	rows[0].PeakVRAM = int64ptr(8192)
	rows[0].LastVRAM = int64ptr(4096)
	rows[0].Launches = 4
	rows[0].MemcpyBytes = 512
	rows[0].WorstSyncUS = 300
	if err := s.FlushProcessGPU(ctx, rows, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var launches, memcpy, worst, peak, last, firstSeen, lastSeen int64
	err = metaDB.QueryRowContext(ctx, "SELECT launches,memcpy_bytes,worst_sync_us,peak_vram_bytes,last_vram_bytes,first_seen_ts,last_seen_ts FROM proc_gpu WHERE proc_id=?", procID).
		Scan(&launches, &memcpy, &worst, &peak, &last, &firstSeen, &lastSeen)
	if err != nil {
		t.Fatal(err)
	}
	if launches != 7 || memcpy != 4608 || worst != 300 || peak != 8192 ||
		last != 4096 || firstSeen != 100 || lastSeen != 300 {
		t.Fatalf("ledger merge = launches:%d memcpy:%d worst:%d peak:%d last:%d times:%d-%d",
			launches, memcpy, worst, peak, last, firstSeen, lastSeen)
	}
	if err := metaDB.QueryRowContext(ctx, "SELECT v FROM daemon_state WHERE k='heartbeat_ts'").Scan(&heartbeat); err != nil {
		t.Fatal(err)
	}
	if heartbeat != strconv.FormatInt(now.Add(2*time.Minute).Unix(), 10) {
		t.Fatalf("successful ledger flush heartbeat = %s", heartbeat)
	}
}

func TestEndProcessRecordsExitOnlyOnce(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testOpenOptions(t.TempDir(), "boot-a", time.Now().UTC(), true))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	metaDB := openTestDB(t, s.MetaPath(), true)
	procID, err := s.UpsertProcess(ctx, ProcessIdentity{
		BootID: "boot-a", TGID: 1001, StartTicks: 2, FirstSeenAt: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	exitCode, signal := int64(0), int64(9)
	if err := s.EndProcess(ctx, procID, 50, "signal", &exitCode, &signal); err != nil {
		t.Fatal(err)
	}
	if err := s.EndProcess(ctx, procID, 60, "exit", &exitCode, nil); err == nil {
		t.Fatal("process exit overwrote the first lifecycle event")
	}
	var reason string
	if err := metaDB.QueryRowContext(ctx, "SELECT end_reason FROM procs WHERE proc_id = ?", procID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "signal" {
		t.Fatalf("stored end reason = %q, want signal", reason)
	}
}
