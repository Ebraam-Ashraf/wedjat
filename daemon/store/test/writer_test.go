package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/store"
	"time"
)

func TestWriteMinuteUpsertsCountersAndNullableMetrics(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 4, 35, 0, time.UTC)
	s, err := Open(ctx, testOpenOptions(root, "boot-a", now, true))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	dayDB := openTestDB(t, s.DayPath(), true)

	first := MinuteBatch{
		GPUSamples: []GPUSample{{
			GPUID: 1, SampleCount: 30,
			UtilGPUSum: int64ptr(300), UtilGPUMax: int64ptr(20),
			UtilMemSum: int64ptr(150), TempMax: int64ptr(60),
			PowerMWSum: int64ptr(300000), PowerMWMax: int64ptr(11000),
			VRAMUsedMax: int64ptr(1024), SMClockMin: int64ptr(500),
			MemClockMin: int64ptr(1000), PowerLimitMW: int64ptr(75000),
			ThrottleOR: int64ptr(1), ECCErrors: int64ptr(2),
		}, {GPUID: 2, SampleCount: 1}},
		Aggregates: []ProcessAggregate{{
			ProcessID: 41, GPUID: 1, Launches: 3, MemcpyCalls: 2,
			MemcpyBytes: 4096, AllocCalls: 1, AllocBytes: 8192,
			FreeBytes: 2048, SyncCalls: 2, SyncUSSum: 180,
			SyncUSMax: 120, IOCTLCalls: 5, UVMFaults: 7, UVMEvicts: 1,
			Errors: 1, VRAMUsedBytes: int64ptr(1024),
		}},
	}
	second := MinuteBatch{
		GPUSamples: []GPUSample{{
			GPUID: 1, SampleCount: 30,
			UtilGPUSum: int64ptr(600), UtilGPUMax: int64ptr(45),
			UtilMemSum: int64ptr(240), TempMax: int64ptr(72),
			PowerMWSum: int64ptr(330000), PowerMWMax: int64ptr(13000),
			VRAMUsedMax: int64ptr(2048), SMClockMin: int64ptr(400),
			MemClockMin: int64ptr(900), PowerLimitMW: nil,
			ThrottleOR: int64ptr(4), ECCErrors: int64ptr(3),
		}},
		Aggregates: []ProcessAggregate{{
			ProcessID: 41, GPUID: 1, Launches: 4, MemcpyCalls: 1,
			MemcpyBytes: 1024, AllocCalls: 2, AllocBytes: 4096,
			FreeBytes: 4096, SyncCalls: 1, SyncUSSum: 80,
			SyncUSMax: 200, IOCTLCalls: 2, UVMFaults: 3, UVMEvicts: 2,
			Errors: 2, VRAMUsedBytes: int64ptr(2048),
		}},
	}
	if err := s.WriteMinuteAt(ctx, now, first); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMinuteAt(ctx, now.Add(20*time.Second), second); err != nil {
		t.Fatal(err)
	}

	var n, utilSum, utilMax, tempMax, powerLimit, throttle, ecc int64
	err = dayDB.QueryRowContext(ctx, "SELECT n, util_gpu_sum, util_gpu_max, temp_max, power_limit_mw, throttle_or, ecc_errors FROM gpu_samples WHERE gpu_id=1").Scan(&n, &utilSum, &utilMax, &tempMax, &powerLimit, &throttle, &ecc)
	if err != nil {
		t.Fatal(err)
	}
	if n != 60 || utilSum != 900 || utilMax != 45 || tempMax != 72 ||
		powerLimit != 75000 || throttle != 5 || ecc != 3 {
		t.Fatalf("merged GPU row = n:%d sum:%d max:%d temp:%d limit:%d throttle:%d ecc:%d",
			n, utilSum, utilMax, tempMax, powerLimit, throttle, ecc)
	}
	var unavailable any
	if err := dayDB.QueryRowContext(ctx, "SELECT power_mw_sum FROM gpu_samples WHERE gpu_id=2").Scan(&unavailable); err != nil {
		t.Fatal(err)
	}
	if unavailable != nil {
		t.Fatalf("unsupported power field stored as %v, want NULL", unavailable)
	}
	var launches, memcpyBytes, syncSum, syncMax, errorsCount, vram int64
	err = dayDB.QueryRowContext(ctx, "SELECT launches, memcpy_bytes, sync_us_sum, sync_us_max, errors, vram_used_bytes FROM agg WHERE proc_id=41").Scan(
		&launches, &memcpyBytes, &syncSum, &syncMax, &errorsCount, &vram)
	if err != nil {
		t.Fatal(err)
	}
	if launches != 7 || memcpyBytes != 5120 || syncSum != 260 ||
		syncMax != 200 || errorsCount != 3 || vram != 2048 {
		t.Fatalf("merged aggregate = launches:%d memcpy:%d sync:%d/%d errors:%d vram:%d",
			launches, memcpyBytes, syncSum, syncMax, errorsCount, vram)
	}
}

func TestWriteMinuteIsAtomicAndRotatesByUTCDate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	start := time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)
	s, err := Open(ctx, testOpenOptions(root, "boot-a", start, true))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	dayDB := openTestDB(t, s.DayPath(), true)

	bad := MinuteBatch{
		GPUSamples: []GPUSample{{GPUID: 1, SampleCount: 1, UtilGPUSum: int64ptr(10)}},
		Aggregates: []ProcessAggregate{{ProcessID: -1, GPUID: 1}},
	}
	if err := s.WriteMinuteAt(ctx, start, bad); err == nil {
		t.Fatal("invalid batch unexpectedly committed")
	}
	var rows int
	if err := dayDB.QueryRowContext(ctx, "SELECT count(*) FROM gpu_samples").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("partial transaction left %d GPU rows", rows)
	}

	oldDay := s.DayPath()
	next := start.Add(2 * time.Minute)
	if err := s.WriteMinuteAt(ctx, next, MinuteBatch{GPUSamples: []GPUSample{{GPUID: 1}}}); err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(s.DayPath()), "2026-10-01.db"; got != want {
		t.Fatalf("rotated file = %s, want %s", got, want)
	}
	if _, err := os.Stat(oldDay); err != nil {
		t.Fatalf("rotation removed prior day file: %v", err)
	}
}
