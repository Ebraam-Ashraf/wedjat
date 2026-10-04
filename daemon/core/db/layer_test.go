package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

func testLayer(t *testing.T) (*Layer, context.Context) {
	t.Helper()
	ctx := context.Background()
	database, err := OpenDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	gpuID, err := database.UpsertGPU(ctx, GPUIdentity{UUID: "GPU-a", Index: 0, PCIBusID: "0000:01:00.0", SeenAtUnix: 1})
	if err != nil {
		t.Fatal(err)
	}
	device := gpuIdentity{device: source.DeviceInfo{UUID: "GPU-a", Index: 0, PCIBusID: "0000:01:00.0"}, id: gpuID}
	l := &Layer{database: database, bootID: "boot-test", devices: []gpuIdentity{device}, processCache: make(map[uint32]processCache), readIdentity: func(uint) (int64, string, error) { return 123, "test", nil }, readEnviron: func(uint32) ([]byte, error) { return []byte("CUDA_VISIBLE_DEVICES=GPU-a\x00"), nil }}
	l.writeBatch = l.WriteBatch
	return l, ctx
}

func TestFoldAggregateByAPI(t *testing.T) {
	var got Aggregate
	for _, row := range []source.AggRow{
		{ApiID: 4, Count: 2}, {ApiID: 7, Count: 3, Bytes: 40}, {ApiID: 5, Count: 4, AllocBytes: 50},
		{ApiID: 6, Count: 7, FreeBytes: 60}, {ApiID: 8, Count: 5, LatencySumNs: 9000, LatencyMaxNs: 7000},
		{ApiID: 12, Count: 8}, {ApiID: 13, Count: 9, UvmFaults: 2}, {ApiID: 18, Count: 10, UvmEvicts: 3},
	} {
		foldAggregate(&got, row)
	}
	want := Aggregate{Launches: 2, MemcpyCalls: 3, MemcpyBytes: 40, AllocCalls: 4, AllocBytes: 50, FreeBytes: 60, SyncCalls: 5, SyncUsSum: 9, SyncUsMax: 7, IoctlCalls: 8, UvmFaults: 2, UvmEvicts: 3}
	if got != want {
		t.Fatalf("folded %#v, want %#v", got, want)
	}
}

func TestFlushRetryPreservesRowsExactlyOnce(t *testing.T) {
	l, _ := testLayer(t)
	l.batch.Aggregates = []pendingAggregate{{key: procKey{1, 1}, minute: 60, row: Aggregate{Launches: 3}}}
	calls := 0
	delivered := map[int64]int{}
	l.writeBatch = func(_ context.Context, b Batch) error {
		calls++
		if calls == 1 {
			return errors.New("injected failure")
		}
		for _, r := range b.Aggregates {
			delivered[r.row.Launches]++
		}
		return nil
	}
	if err := l.flush(context.Background()); err == nil {
		t.Fatal("first flush should fail")
	}
	if got := len(l.batch.Aggregates); got != 1 {
		t.Fatalf("retained %d aggregate rows after failure", got)
	}
	l.batch.Incidents = append(l.batch.Incidents, Incident{Type: "test", DedupeKey: "new", FirstTS: 1, LastTS: 1})
	if err := l.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || delivered[3] != 1 {
		t.Fatalf("calls=%d delivered=%v", calls, delivered)
	}
}

func TestStopReportsFinalFlushFailure(t *testing.T) {
	l, _ := testLayer(t)
	ctx, cancel := context.WithCancel(context.Background())
	l.stop = cancel
	l.stopped = make(chan struct{})
	l.batch.Incidents = []Incident{{Type: "test", DedupeKey: "final", FirstTS: 1, LastTS: 1}}
	l.writeBatch = func(context.Context, Batch) error { return errors.New("disk failure") }
	go l.run(ctx, source.NewChans(1))
	if err := l.Stop(); err == nil {
		t.Fatal("Stop hid final flush failure")
	}
}

func TestWriteBatchUsesOneTransactionPerDatabase(t *testing.T) {
	l, ctx := testLayer(t)
	proc := ProcessIdentity{BootID: l.bootID, TGID: 10, StartTicks: 22, Command: "p", FirstSeenUnix: 1}
	key := procKey{10, 22}
	if _, err := l.database.day.Exec(`CREATE TRIGGER reject_gpu BEFORE INSERT ON gpu_samples BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	b := Batch{Processes: []ProcessIdentity{proc}, GPUMinutes: []timedGPU{{sample: GPUMinuteSample{GPUID: l.devices[0].id}}}}
	if err := l.WriteBatch(ctx, b); err == nil {
		t.Fatal("expected daily write to fail")
	}
	var metaCount, dayCount int
	if err := l.database.meta.QueryRow(`SELECT COUNT(*) FROM procs WHERE boot_id=? AND tgid=?`, l.bootID, key.Tgid).Scan(&metaCount); err != nil {
		t.Fatal(err)
	}
	if err := l.database.day.QueryRow(`SELECT COUNT(*) FROM gpu_samples`).Scan(&dayCount); err != nil {
		t.Fatal(err)
	}
	if metaCount != 0 || dayCount != 0 {
		t.Fatalf("partial batch persisted: meta=%d daily=%d", metaCount, dayCount)
	}
}

func TestProcessExitAndLateAggregate(t *testing.T) {
	l, ctx := testLayer(t)
	const pid = 42
	l.handleEvent(source.Event{ApiID: 16, Tgid: pid, TsNano: uint64(time.Now().UnixNano())})
	l.handleEvent(source.Event{ApiID: 17, Tgid: pid, StartBoottimeNs: 1_230_000_000, TsNano: uint64(time.Now().UnixNano()), Status: 7 << 8})
	l.handleAggregates([]source.AggRow{{TsNano: time.Now().UnixNano(), Tgid: pid, Ordinal: 0, ApiID: 4, Count: 2}})
	if len(l.batch.Aggregates) != 1 {
		t.Fatalf("late aggregate was not attributed: %#v", l.batch.Aggregates)
	}
	if err := l.flush(ctx); err != nil {
		t.Fatal(err)
	}
	var endTS int64
	var exitCode, termSignal sql.NullInt64
	if err := l.database.meta.QueryRow(`SELECT end_ts,exit_code,term_signal FROM procs WHERE tgid=?`, pid).Scan(&endTS, &exitCode, &termSignal); err != nil {
		t.Fatal(err)
	}
	if endTS == 0 || !exitCode.Valid || exitCode.Int64 != 7 || termSignal.Valid {
		t.Fatalf("exit status end=%d code=%v signal=%v", endTS, exitCode, termSignal)
	}
	var launches int
	if err := l.database.day.QueryRow(`SELECT launches FROM agg`).Scan(&launches); err != nil {
		t.Fatal(err)
	}
	if launches != 2 {
		t.Fatalf("launches=%d, want 2", launches)
	}
	l.readIdentity = func(pid uint) (int64, string, error) {
		if pid == 43 {
			return 124, "signal", nil
		}
		return 123, "test", nil
	}
	l.handleEvent(source.Event{ApiID: 16, Tgid: 43, TsNano: uint64(time.Now().UnixNano())})
	l.handleEvent(source.Event{ApiID: 17, Tgid: 43, StartBoottimeNs: 1_240_000_000, TsNano: uint64(time.Now().UnixNano()), Status: 9})
	if err := l.flush(ctx); err != nil {
		t.Fatal(err)
	}
	var signalCode, signalExit sql.NullInt64
	if err := l.database.meta.QueryRow(`SELECT exit_code,term_signal FROM procs WHERE tgid=43`).Scan(&signalExit, &signalCode); err != nil {
		t.Fatal(err)
	}
	if signalExit.Valid || !signalCode.Valid || signalCode.Int64 != 9 {
		t.Fatalf("signal status exit=%v signal=%v", signalExit, signalCode)
	}
}

func TestSweepRequiresLatestCompleteListButAllowsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name       string
		complete   bool
		wantClosed bool
	}{{"complete", true, true}, {"incomplete", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			l, ctx := testLayer(t)
			id, err := l.database.UpsertProcess(ctx, ProcessIdentity{BootID: l.bootID, TGID: 91, StartTicks: 5, Command: "old", FirstSeenUnix: 1})
			if err != nil {
				t.Fatal(err)
			}
			l.handleProcs(source.ProcList{TsNano: time.Now().UnixNano(), Complete: tc.complete})
			if err := l.flush(ctx); err != nil {
				t.Fatal(err)
			}
			var end sql.NullInt64
			if err := l.database.meta.QueryRow(`SELECT end_ts FROM procs WHERE proc_id=?`, id).Scan(&end); err != nil {
				t.Fatal(err)
			}
			if end.Valid != tc.wantClosed {
				t.Fatalf("closed=%v want %v", end.Valid, tc.wantClosed)
			}
		})
	}
}

func TestResolveCUDAOrdinalsAndPIDReuse(t *testing.T) {
	devices := []gpuIdentity{{device: source.DeviceInfo{UUID: "GPU-a", Index: 0, PCIBusID: "0000:02:00.0"}, id: 10}, {device: source.DeviceInfo{UUID: "GPU-b", Index: 1, PCIBusID: "0000:01:00.0"}, id: 11}}
	cases := []struct {
		name string
		env  string
		want map[uint32]int64
	}{
		{"UUID list", "CUDA_VISIBLE_DEVICES=GPU-b,GPU-a\x00", map[uint32]int64{0: 11, 1: 10}},
		{"single GPU integer", "CUDA_VISIBLE_DEVICES=0\x00", map[uint32]int64{}},
		{"ambiguous unset", "", map[uint32]int64{}},
		{"PCI order", "CUDA_DEVICE_ORDER=PCI_BUS_ID\x00", map[uint32]int64{0: 11, 1: 10}},
		{"PCI ordered integer selection", "CUDA_VISIBLE_DEVICES=0,1\x00CUDA_DEVICE_ORDER=PCI_BUS_ID\x00", map[uint32]int64{0: 11, 1: 10}},
		{"empty", "CUDA_VISIBLE_DEVICES=\x00", map[uint32]int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveCUDAOrdinals([]byte(tc.env), devices)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
	single := resolveCUDAOrdinals([]byte("CUDA_VISIBLE_DEVICES=0\x00"), devices[:1])
	if single[0] != 10 {
		t.Fatalf("single GPU token resolved to %v", single)
	}
	l, _ := testLayer(t)
	l.readEnviron = func(uint32) ([]byte, error) { return []byte("CUDA_VISIBLE_DEVICES=GPU-a\x00"), nil }
	l.readIdentity = func(uint) (int64, string, error) { return 124, "reused", nil }
	pc := processCache{key: procKey{Tgid: 9, StartTicks: 123}}
	if m := l.resolveOrdinals(&pc); len(m) != 0 {
		t.Fatalf("PID-reused environment was cached: %v", m)
	}
}

func TestStartTicksMatchBoottime(t *testing.T) {
	if !startTicksMatchBoottime(1_230_000_000, 123) {
		t.Fatal("expected matching ticks")
	}
	if startTicksMatchBoottime(0, 123) || startTicksMatchBoottime(1_230_000_000, 124) {
		t.Fatal("accepted mismatched/zero identity")
	}
}
