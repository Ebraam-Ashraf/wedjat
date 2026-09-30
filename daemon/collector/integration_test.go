package collector_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/collector"
	"github.com/Ebraam-Ashraf/wedjat/daemon/store"
	_ "modernc.org/sqlite"
)

func openMeta(t *testing.T, path string) *sql.DB {
	t.Helper()
	values := url.Values{}
	values.Set("mode", "ro")
	values.Add("_pragma", "query_only(1)")
	values.Add("_pragma", "busy_timeout(5000)")
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: values.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEndToEndLedgerAttribution is the regression test for the two bugs that
// made stored rows unattributable:
//
//  1. agg.proc_id held a raw OS PID instead of a ledger proc_id, so every join
//     against the procs table returned nothing.
//  2. gpus held only the reserved UNKNOWN row, so gpu_samples referenced a
//     device that did not describe real hardware.
//
// It drives the exported registration path against a real store and then checks
// that the identifiers written by the collector actually resolve.
func TestEndToEndLedgerAttribution(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	boot := "boot-integration"
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	st, err := store.Open(ctx, store.OpenOptions{
		DataDir:     filepath.Join(root, "data"),
		LockPath:    filepath.Join(root, "run", "daemon.lock"),
		ResetOnBoot: true,
		BootID:      func() (string, error) { return boot, nil },
		Clock:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)

	// Register a GPU the way the collector does, then confirm the row is real
	// hardware rather than the UNKNOWN sentinel.
	gpuID, err := st.UpsertGPU(ctx, store.GPUIdentity{
		UUID:   "GPU-integration-test",
		SeenAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gpuID == 0 {
		t.Fatal("a real GPU must not be assigned the reserved gpu_id 0")
	}

	meta := openMeta(t, st.MetaPath())

	// Register this test process, exactly as the identity cache would.
	self := os.Getpid()
	identity, err := ReadProcIdentity(self)
	if err != nil {
		t.Fatal(err)
	}
	procID, err := st.UpsertProcess(ctx, store.ProcessIdentity{
		BootID:      boot,
		TGID:        int64(identity.TGID),
		StartTicks:  identity.StartTicks,
		Command:     identity.Command,
		Cmdline:     identity.Cmdline,
		FirstSeenAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if procID == 0 {
		t.Fatal("a real process must not be assigned the reserved proc_id 0")
	}

	if err := st.MapProcessDevice(ctx, procID, 0, gpuID); err != nil {
		t.Fatal(err)
	}

	// The critical join: an agg row keyed by these identifiers must resolve
	// to a named process and a named GPU. Under the old behaviour proc_id was
	// the raw PID and this query returned nothing.
	if err := st.WriteMinuteAt(ctx, now.Add(time.Minute), store.MinuteBatch{
		GPUSamples: []store.GPUSample{{GPUID: gpuID, SampleCount: 30}},
		Aggregates: []store.ProcessAggregate{{
			ProcessID:     procID,
			GPUID:         gpuID,
			VRAMUsedBytes: func() *int64 { v := int64(4096); return &v }(),
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// The critical join: an agg row keyed by these identifiers must resolve
	// to a named process and a named GPU. Under the old behaviour proc_id held
	// the raw PID and this query returned nothing.
	//
	// agg lives in the daily database while procs and gpus live in meta, so
	// the day file is attached to join across both.
	if _, err := meta.ExecContext(ctx, "ATTACH DATABASE ? AS day", st.DayPath()); err != nil {
		t.Fatal(err)
	}
	defer meta.ExecContext(ctx, "DETACH DATABASE day")

	var command, gpuUUID string
	err = meta.QueryRowContext(ctx, `
		SELECT p.command, g.uuid
		FROM day.agg a
		JOIN procs p ON p.proc_id = a.proc_id
		JOIN gpus  g ON g.gpu_id  = a.gpu_id
		WHERE a.ts = ?`, now.Add(time.Minute).Unix()).
		Scan(&command, &gpuUUID)
	if err != nil {
		t.Fatalf("agg row does not join to identity tables: %v", err)
	}
	if command == "" || command == "<unattributed>" {
		t.Fatalf("agg row resolved to %q, want a real command", command)
	}
	if gpuUUID != "GPU-integration-test" {
		t.Fatalf("agg row resolved to GPU %q", gpuUUID)
	}

	// The reserved rows must still be present for unattributed data.
	var reservedGPU, reservedProc int
	if err := meta.QueryRowContext(ctx, "SELECT count(*) FROM gpus WHERE gpu_id=0 AND uuid='UNKNOWN'").Scan(&reservedGPU); err != nil {
		t.Fatal(err)
	}
	if err := meta.QueryRowContext(ctx, "SELECT count(*) FROM procs WHERE proc_id=0 AND command='<unattributed>'").Scan(&reservedProc); err != nil {
		t.Fatal(err)
	}
	if reservedGPU != 1 || reservedProc != 1 {
		t.Fatalf("reserved rows missing: gpu=%d proc=%d", reservedGPU, reservedProc)
	}
}

// TestPIDReuseProducesDistinctLedgerRows pins the reason startTicks is part of
// the identity key. Two different processes reusing one PID must not collapse
// onto a single procs row, or historical data would be silently reassigned.
func TestPIDReuseProducesDistinctLedgerRows(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	boot := "boot-reuse"
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	st, err := store.Open(ctx, store.OpenOptions{
		DataDir:     filepath.Join(root, "data"),
		LockPath:    filepath.Join(root, "run", "daemon.lock"),
		ResetOnBoot: true,
		BootID:      func() (string, error) { return boot, nil },
		Clock:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)

	// Same boot, same TGID, different start times: two distinct processes that
	// happened to share a PID.
	first, err := st.UpsertProcess(ctx, store.ProcessIdentity{
		BootID: boot, TGID: 4242, StartTicks: 100, Command: "first", FirstSeenAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.UpsertProcess(ctx, store.ProcessIdentity{
		BootID: boot, TGID: 4242, StartTicks: 200, Command: "second", FirstSeenAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("a reused PID collapsed onto one row (id %d)", first)
	}

	// Re-registering the same instance must be idempotent, otherwise every
	// flush would allocate a new row for a still-running process.
	again, err := st.UpsertProcess(ctx, store.ProcessIdentity{
		BootID: boot, TGID: 4242, StartTicks: 100, Command: "first", FirstSeenAt: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("re-registering the same process changed id from %d to %d", first, again)
	}

	meta := openMeta(t, st.MetaPath())
	var rows int
	if err := meta.QueryRowContext(ctx, "SELECT count(*) FROM procs WHERE tgid=4242").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("expected 2 rows for the reused pid, got %d", rows)
	}
}
