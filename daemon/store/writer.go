package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type GPUSample struct {
	GPUID        int64
	SampleCount  int64
	UtilGPUSum   *int64
	UtilGPUMax   *int64
	UtilMemSum   *int64
	TempMax      *int64
	PowerMWSum   *int64
	PowerMWMax   *int64
	VRAMUsedMax  *int64
	SMClockMin   *int64
	MemClockMin  *int64
	PowerLimitMW *int64
	ThrottleOR   *int64
	ECCErrors    *int64
}

type ProcessAggregate struct {
	ProcessID     int64
	GPUID         int64
	Launches      int64
	MemcpyCalls   int64
	MemcpyBytes   int64
	AllocCalls    int64
	AllocBytes    int64
	FreeBytes     int64
	SyncCalls     int64
	SyncUSSum     int64
	SyncUSMax     int64
	IOCTLCalls    int64
	UVMFaults     int64
	UVMEvicts     int64
	Errors        int64
	VRAMUsedBytes *int64
}

type MinuteBatch struct {
	GPUSamples []GPUSample
	Aggregates []ProcessAggregate
}

func (s *Store) WriteMinute(ctx context.Context, batch MinuteBatch) error {
	return s.WriteMinuteAt(ctx, s.clock(), batch)
}

// WriteMinuteAt writes one already-aggregated interval into its UTC minute.
// Repeated writes for the same minute merge sums/counters and preserve extrema.
func (s *Store) WriteMinuteAt(ctx context.Context, at time.Time, batch MinuteBatch) error {
	if ctx == nil {
		return errors.New("store: nil write context")
	}
	if err := validateMinuteBatch(batch); err != nil {
		return err
	}
	minute := at.UTC().Truncate(time.Minute).Unix()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.day == nil {
		return errors.New("store: write after close")
	}
	if err := s.rotateDay(ctx, at.UTC()); err != nil {
		return err
	}
	tx, err := s.day.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	gpuStmt, err := tx.PrepareContext(ctx, gpuSampleUpsert)
	if err != nil {
		return err
	}
	for _, sample := range batch.GPUSamples {
		n := sample.SampleCount
		if n == 0 {
			n = 1
		}
		if _, err := gpuStmt.ExecContext(ctx,
			minute, sample.GPUID, n,
			nullableInt(sample.UtilGPUSum), nullableInt(sample.UtilGPUMax),
			nullableInt(sample.UtilMemSum), nullableInt(sample.TempMax),
			nullableInt(sample.PowerMWSum), nullableInt(sample.PowerMWMax),
			nullableInt(sample.VRAMUsedMax), nullableInt(sample.SMClockMin),
			nullableInt(sample.MemClockMin), nullableInt(sample.PowerLimitMW),
			nullableInt(sample.ThrottleOR), nullableInt(sample.ECCErrors),
		); err != nil {
			gpuStmt.Close()
			return fmt.Errorf("upsert GPU sample: %w", err)
		}
	}
	if err := gpuStmt.Close(); err != nil {
		return err
	}

	aggStmt, err := tx.PrepareContext(ctx, aggregateUpsert)
	if err != nil {
		return err
	}
	for _, agg := range batch.Aggregates {
		if _, err := aggStmt.ExecContext(ctx,
			minute, agg.ProcessID, agg.GPUID,
			agg.Launches, agg.MemcpyCalls, agg.MemcpyBytes,
			agg.AllocCalls, agg.AllocBytes, agg.FreeBytes,
			agg.SyncCalls, agg.SyncUSSum, agg.SyncUSMax,
			agg.IOCTLCalls, agg.UVMFaults, agg.UVMEvicts, agg.Errors,
			nullableInt(agg.VRAMUsedBytes),
		); err != nil {
			aggStmt.Close()
			return fmt.Errorf("upsert process aggregate: %w", err)
		}
	}
	if err := aggStmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

const gpuSampleUpsert = `INSERT INTO gpu_samples (
	ts, gpu_id, n, util_gpu_sum, util_gpu_max, util_mem_sum,
	temp_max, power_mw_sum, power_mw_max, vram_used_max,
	sm_clock_min, mem_clock_min, power_limit_mw, throttle_or, ecc_errors
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(ts, gpu_id) DO UPDATE SET
	n = gpu_samples.n + excluded.n,
	util_gpu_sum = CASE WHEN excluded.util_gpu_sum IS NULL THEN gpu_samples.util_gpu_sum
		WHEN gpu_samples.util_gpu_sum IS NULL THEN excluded.util_gpu_sum
		ELSE gpu_samples.util_gpu_sum + excluded.util_gpu_sum END,
	util_gpu_max = CASE WHEN excluded.util_gpu_max IS NULL THEN gpu_samples.util_gpu_max
		WHEN gpu_samples.util_gpu_max IS NULL THEN excluded.util_gpu_max
		ELSE MAX(gpu_samples.util_gpu_max, excluded.util_gpu_max) END,
	util_mem_sum = CASE WHEN excluded.util_mem_sum IS NULL THEN gpu_samples.util_mem_sum
		WHEN gpu_samples.util_mem_sum IS NULL THEN excluded.util_mem_sum
		ELSE gpu_samples.util_mem_sum + excluded.util_mem_sum END,
	temp_max = CASE WHEN excluded.temp_max IS NULL THEN gpu_samples.temp_max
		WHEN gpu_samples.temp_max IS NULL THEN excluded.temp_max
		ELSE MAX(gpu_samples.temp_max, excluded.temp_max) END,
	power_mw_sum = CASE WHEN excluded.power_mw_sum IS NULL THEN gpu_samples.power_mw_sum
		WHEN gpu_samples.power_mw_sum IS NULL THEN excluded.power_mw_sum
		ELSE gpu_samples.power_mw_sum + excluded.power_mw_sum END,
	power_mw_max = CASE WHEN excluded.power_mw_max IS NULL THEN gpu_samples.power_mw_max
		WHEN gpu_samples.power_mw_max IS NULL THEN excluded.power_mw_max
		ELSE MAX(gpu_samples.power_mw_max, excluded.power_mw_max) END,
	vram_used_max = CASE WHEN excluded.vram_used_max IS NULL THEN gpu_samples.vram_used_max
		WHEN gpu_samples.vram_used_max IS NULL THEN excluded.vram_used_max
		ELSE MAX(gpu_samples.vram_used_max, excluded.vram_used_max) END,
	sm_clock_min = CASE WHEN excluded.sm_clock_min IS NULL THEN gpu_samples.sm_clock_min
		WHEN gpu_samples.sm_clock_min IS NULL THEN excluded.sm_clock_min
		ELSE MIN(gpu_samples.sm_clock_min, excluded.sm_clock_min) END,
	mem_clock_min = CASE WHEN excluded.mem_clock_min IS NULL THEN gpu_samples.mem_clock_min
		WHEN gpu_samples.mem_clock_min IS NULL THEN excluded.mem_clock_min
		ELSE MIN(gpu_samples.mem_clock_min, excluded.mem_clock_min) END,
	power_limit_mw = COALESCE(excluded.power_limit_mw, gpu_samples.power_limit_mw),
	throttle_or = CASE WHEN excluded.throttle_or IS NULL THEN gpu_samples.throttle_or
		WHEN gpu_samples.throttle_or IS NULL THEN excluded.throttle_or
		ELSE gpu_samples.throttle_or | excluded.throttle_or END,
	ecc_errors = CASE WHEN excluded.ecc_errors IS NULL THEN gpu_samples.ecc_errors
		WHEN gpu_samples.ecc_errors IS NULL THEN excluded.ecc_errors
		ELSE MAX(gpu_samples.ecc_errors, excluded.ecc_errors) END`

const aggregateUpsert = `INSERT INTO agg (
	ts, proc_id, gpu_id, launches, memcpy_calls, memcpy_bytes,
	alloc_calls, alloc_bytes, free_bytes, sync_calls, sync_us_sum,
	sync_us_max, ioctl_calls, uvm_faults, uvm_evicts, errors, vram_used_bytes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
	errors = agg.errors + excluded.errors,
	vram_used_bytes = CASE WHEN excluded.vram_used_bytes IS NULL THEN agg.vram_used_bytes
		WHEN agg.vram_used_bytes IS NULL THEN excluded.vram_used_bytes
		ELSE MAX(agg.vram_used_bytes, excluded.vram_used_bytes) END`

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func validateMinuteBatch(batch MinuteBatch) error {
	for i, sample := range batch.GPUSamples {
		if sample.GPUID < 0 || sample.SampleCount < 0 {
			return fmt.Errorf("GPU sample %d has a negative id or sample count", i)
		}
		for _, value := range []*int64{
			sample.UtilGPUSum, sample.UtilGPUMax, sample.UtilMemSum,
			sample.TempMax, sample.PowerMWSum, sample.PowerMWMax,
			sample.VRAMUsedMax, sample.SMClockMin, sample.MemClockMin,
			sample.PowerLimitMW, sample.ThrottleOR, sample.ECCErrors,
		} {
			if value != nil && *value < 0 {
				return fmt.Errorf("GPU sample %d contains a negative metric", i)
			}
		}
	}
	for i, agg := range batch.Aggregates {
		if agg.ProcessID < 0 || agg.GPUID < 0 {
			return fmt.Errorf("aggregate %d has a negative identity", i)
		}
		for _, value := range []int64{
			agg.Launches, agg.MemcpyCalls, agg.MemcpyBytes, agg.AllocCalls,
			agg.AllocBytes, agg.FreeBytes, agg.SyncCalls, agg.SyncUSSum,
			agg.SyncUSMax, agg.IOCTLCalls, agg.UVMFaults, agg.UVMEvicts, agg.Errors,
		} {
			if value < 0 {
				return fmt.Errorf("aggregate %d contains a negative counter", i)
			}
		}
		if agg.VRAMUsedBytes != nil && *agg.VRAMUsedBytes < 0 {
			return fmt.Errorf("aggregate %d has negative VRAM", i)
		}
	}
	return nil
}
