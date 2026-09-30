package collector

/*
#cgo CFLAGS: -I../nvml -I../ebpf -I../ebpf/build -I/usr/local/cuda/include -I/usr/local/cuda/targets/x86_64-linux/include -Wno-deprecated-declarations
#cgo LDFLAGS: -L/usr/lib/x86_64-linux-gnu -lnvidia-ml -lbpf -lelf -lz
#include "bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"
	"unsafe"

	"github.com/Ebraam-Ashraf/wedjat/daemon/bootstrap"
	"github.com/Ebraam-Ashraf/wedjat/daemon/store"
)

const (
	// pollInterval is how often we sample NVML.
	pollInterval = 2 * time.Second

	// flushInterval is how often we aggregate and write to the store.
	flushInterval = 1 * time.Minute

	// heartbeatInterval is how often the store's liveness marker advances.
	// It must stay well below any reader's staleness threshold, so a viewer
	// polling the database can tell a live daemon from a dead one.
	heartbeatInterval = 10 * time.Second
)

// gpuState holds the discovered UUID for one GPU.
type gpuState struct {
	index int
	uuid  string
}

// accumulator collects per-GPU samples between flushes.
type accumulator struct {
	// per-GPU accumulators keyed by GPU index
	gpus map[int]*gpuAccum

	// per-(pid, gpuIndex) process accumulators
	procs map[procKey]*procAccum
}

type gpuAccum struct {
	count       int64
	utilGPUSum  int64
	utilGPUMax  int64
	utilMemSum  int64
	tempMax     int64
	powerMWSum  int64
	powerMWMax  int64
	vramUsedMax int64
	smClockMin  int64
	memClockMin int64
	powerLimit  int64
	throttleOR  int64
	eccErrors   int64
	// track which fields we've ever seen
	hasUtil     bool
	hasMem      bool
	hasTemp     bool
	hasPower    bool
	hasSMClock  bool
	hasMemClock bool
	hasLimit    bool
	hasThrottle bool
	hasECC      bool
	hasVRAMUsed bool
}

type procKey struct {
	pid      int
	gpuIndex int
}

type procAccum struct {
	vramMax int64
	hasVRAM bool
}

func newAccumulator() *accumulator {
	return &accumulator{
		gpus:  make(map[int]*gpuAccum),
		procs: make(map[procKey]*procAccum),
	}
}

// Handle exposes the running collector to other daemon stages.
//
// The live socket feed needs the in-memory accumulator that sample() folds into
// the database every flush interval. That state is unreachable from outside the
// poll loop, so it is published here.
type Handle struct {
	stop  bootstrap.StopFunc
	live  *liveState
	empty bool
}

// Live returns a point-in-time copy of the most recent sample.
func (h *Handle) Live() LiveState {
	if h == nil || h.empty {
		return LiveState{}
	}
	return h.live.read()
}

// Stop shuts the collector down.
func (h *Handle) Stop(ctx context.Context) error {
	if h == nil || h.stop == nil {
		return nil
	}
	return h.stop(ctx)
}

// Start initializes NVML, discovers GPUs, and starts the polling goroutine.
// The returned Handle exposes both shutdown and the live sample.
func Start(ctx context.Context, st *store.Store) (*Handle, error) {
	log.Println("[collector] Initializing NVML...")

	res := C.collector_init()
	if res != 0 {
		log.Printf("[collector] WARNING: NVML init failed (rc=%d). Running without GPU telemetry.", res)
		// Return an inert handle; the daemon still works for eBPF-only or store-only testing.
		return &Handle{empty: true}, nil
	}

	// Discover GPUs
	gpus := discoverGPUs()
	if len(gpus) == 0 {
		log.Println("[collector] WARNING: No GPUs found. Polling disabled.")
		C.collector_shutdown()
		return &Handle{empty: true}, nil
	}

	for _, g := range gpus {
		log.Printf("[collector] GPU %d: %s", g.index, g.uuid)
	}

	// Start the polling goroutine
	pollCtx, pollCancel := context.WithCancel(ctx)
	live := &liveState{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollLoop(pollCtx, st, gpus, live)
	}()

	stopFunc := func(ctx context.Context) error {
		log.Println("[collector] Shutting down...")
		pollCancel()
		wg.Wait()
		C.collector_shutdown()
		log.Println("[collector] Shutdown complete.")
		return nil
	}

	log.Printf("[collector] Polling %d GPU(s) every %s, flushing every %s",
		len(gpus), pollInterval, flushInterval)
	return &Handle{stop: stopFunc, live: live}, nil
}

// discoverGPUs enumerates NVIDIA GPUs and returns their UUIDs.
func discoverGPUs() []gpuState {
	count := int(C.collector_device_count())
	if count <= 0 {
		return nil
	}

	gpus := make([]gpuState, 0, count)
	for i := 0; i < count; i++ {
		var uuidBuf [96]C.char
		rc := C.collector_device_uuid(C.uint(i), &uuidBuf[0])
		if rc != 0 {
			log.Printf("[collector] Failed to get UUID for GPU %d, skipping", i)
			continue
		}
		gpus = append(gpus, gpuState{
			index: i,
			uuid:  C.GoString(&uuidBuf[0]),
		})
	}
	return gpus
}

// pollLoop is the main goroutine. It samples NVML every pollInterval
// and flushes aggregated data to the store every flushInterval.
func pollLoop(ctx context.Context, st *store.Store, gpus []gpuState, live *liveState) {
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()

	flushTicker := time.NewTicker(flushInterval)
	defer flushTicker.Stop()

	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	acc := newAccumulator()
	identities := newIdentityCache()
	// The ledger needs a stable gpu_id for every device before the first
	// flush, and per-minute rows must reference that key rather than the
	// NVML index, so register identity up front.
	if err := registerGPUs(ctx, st, gpus, identities); err != nil {
		log.Printf("[collector] WARNING: failed to register GPU identity: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			// Final flush before exit
			flush(ctx, st, acc, gpus, identities)
			return

		case <-pollTicker.C:
			sample(acc, gpus, live)

		case <-heartbeatTicker.C:
			if err := st.Heartbeat(ctx); err != nil {
				log.Printf("[collector] Heartbeat failed: %v", err)
			}

		case <-flushTicker.C:
			flush(ctx, st, acc, gpus, identities)
			acc = newAccumulator()
		}
	}
}

// registerGPUs persists GPU identity so the meta database describes real
// hardware instead of only the reserved UNKNOWN row, and records the resulting
// gpu_id for the identity cache.
func registerGPUs(ctx context.Context, st *store.Store, gpus []gpuState, identities *identityCache) error {
	now := time.Now().UTC().Unix()
	var errs []error
	for _, g := range gpus {
		idx := int64(g.index)
		gpuID, err := st.UpsertGPU(ctx, store.GPUIdentity{
			UUID:     g.uuid,
			Index:    &idx,
			SeenAt:   now,
			LastSeen: now,
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		identities.setGPUID(g.index, gpuID)
	}
	return errors.Join(errs...)
}

// sample takes one NVML snapshot per GPU and folds it into the accumulator.
func sample(acc *accumulator, gpus []gpuState, live *liveState) {
	now := time.Now()
	liveGPUs := make([]GPULive, 0, len(gpus))
	liveProcs := make([]ProcLive, 0, len(gpus)*8)

	for _, g := range gpus {
		cuuid := C.CString(g.uuid)

		// GPU-level snapshot
		var snap C.struct_collector_gpu_snap
		C.collector_snapshot_gpu(cuuid, &snap)
		if snap.ok != 0 {
			foldGPU(acc, g.index, &snap)
			liveGPUs = append(liveGPUs, GPULive{
				Index:       g.index,
				UtilGPU:     int(snap.gpu_util),
				UtilMem:     int(snap.mem_util),
				TempC:       int(snap.temp_c),
				PowerMW:     int(snap.power_mw),
				VRAMUsed:    int64(snap.mem_used),
				SMClockMHz:  int(snap.sm_clock_mhz),
				MemClockMHz: int(snap.mem_clock_mhz),
				Valid:       true,
			})
		}

		// Process-level snapshot
		var procs C.struct_collector_proc_list
		C.collector_snapshot_procs(cuuid, &procs)
		if procs.ok != 0 {
			foldProcs(acc, g.index, &procs)
			for i := 0; i < int(procs.count); i++ {
				entry := procs.entries[i]
				liveProcs = append(liveProcs, ProcLive{
					PID:       int(entry.pid),
					GPUIndex:  g.index,
					VRAMBytes: int64(entry.vram_bytes),
					VRAMValid: entry.vram_valid != 0,
				})
			}
		}

		C.free(unsafe.Pointer(cuuid))
	}

	sort.Slice(liveGPUs, func(i, j int) bool { return liveGPUs[i].Index < liveGPUs[j].Index })
	sort.Slice(liveProcs, func(i, j int) bool {
		if liveProcs[i].GPUIndex != liveProcs[j].GPUIndex {
			return liveProcs[i].GPUIndex < liveProcs[j].GPUIndex
		}
		return liveProcs[i].PID < liveProcs[j].PID
	})
	live.publish(liveGPUs, liveProcs, now)
}

// GPU field validity flags — must match the C enum in poller.h
const (
	cValidGPUUtil   = 1 << 1
	cValidMemUtil   = 1 << 2
	cValidMemUsed   = 1 << 3
	cValidTemp      = 1 << 6
	cValidPower     = 1 << 7
	cValidSMClock   = 1 << 8
	cValidMemClock  = 1 << 9
	cValidThrottle  = 1 << 10
	cValidPowerLim  = 1 << 11
	cValidECCUncorr = 1 << 13
)

func foldGPU(acc *accumulator, gpuIndex int, snap *C.struct_collector_gpu_snap) {
	ga, ok := acc.gpus[gpuIndex]
	if !ok {
		ga = &gpuAccum{
			smClockMin:  1 << 62,
			memClockMin: 1 << 62,
		}
		acc.gpus[gpuIndex] = ga
	}

	ga.count++
	vf := uint64(snap.valid_fields)

	if vf&cValidGPUUtil != 0 {
		v := int64(snap.gpu_util)
		ga.utilGPUSum += v
		if !ga.hasUtil || v > ga.utilGPUMax {
			ga.utilGPUMax = v
		}
		ga.hasUtil = true
	}
	if vf&cValidMemUtil != 0 {
		ga.utilMemSum += int64(snap.mem_util)
		ga.hasMem = true
	}
	if vf&cValidMemUsed != 0 {
		v := int64(snap.mem_used)
		if !ga.hasVRAMUsed || v > ga.vramUsedMax {
			ga.vramUsedMax = v
		}
		ga.hasVRAMUsed = true
	}
	if vf&cValidTemp != 0 {
		v := int64(snap.temp_c)
		if !ga.hasTemp || v > ga.tempMax {
			ga.tempMax = v
		}
		ga.hasTemp = true
	}
	if vf&cValidPower != 0 {
		v := int64(snap.power_mw)
		ga.powerMWSum += v
		if !ga.hasPower || v > ga.powerMWMax {
			ga.powerMWMax = v
		}
		ga.hasPower = true
	}
	if vf&cValidSMClock != 0 {
		v := int64(snap.sm_clock_mhz)
		if !ga.hasSMClock || v < ga.smClockMin {
			ga.smClockMin = v
		}
		ga.hasSMClock = true
	}
	if vf&cValidMemClock != 0 {
		v := int64(snap.mem_clock_mhz)
		if !ga.hasMemClock || v < ga.memClockMin {
			ga.memClockMin = v
		}
		ga.hasMemClock = true
	}
	if vf&cValidPowerLim != 0 {
		ga.powerLimit = int64(snap.power_limit_mw)
		ga.hasLimit = true
	}
	if vf&cValidThrottle != 0 {
		ga.throttleOR |= int64(snap.throttle_reasons)
		ga.hasThrottle = true
	}
	if vf&cValidECCUncorr != 0 {
		v := int64(snap.ecc_errors)
		if !ga.hasECC || v > ga.eccErrors {
			ga.eccErrors = v
		}
		ga.hasECC = true
	}
}

// unattributedProcID is the store's reserved procs row for data that cannot be
// tied to a real process.
const unattributedProcID = 0

// mergeProcAccum folds pa into the accumulator for key, keeping the peak VRAM
// across every process sharing that key. This is how several unattributed
// processes collapse onto the store's single reserved row.
func mergeProcAccum(acc map[procKey]*procAccum, key procKey, pa *procAccum) *procAccum {
	existing, ok := acc[key]
	if !ok {
		holder := &procAccum{vramMax: pa.vramMax, hasVRAM: pa.hasVRAM}
		acc[key] = holder
		return holder
	}
	if pa.hasVRAM && (!existing.hasVRAM || pa.vramMax > existing.vramMax) {
		existing.vramMax = pa.vramMax
		existing.hasVRAM = true
	}
	return existing
}

func foldProcs(acc *accumulator, gpuIndex int, procs *C.struct_collector_proc_list) {
	for i := 0; i < int(procs.count); i++ {
		entry := procs.entries[i]
		key := procKey{pid: int(entry.pid), gpuIndex: gpuIndex}

		pa, ok := acc.procs[key]
		if !ok {
			pa = &procAccum{}
			acc.procs[key] = pa
		}

		if entry.vram_valid != 0 {
			v := int64(entry.vram_bytes)
			if !pa.hasVRAM || v > pa.vramMax {
				pa.vramMax = v
			}
			pa.hasVRAM = true
		}
	}
}

// flush converts the accumulator into a MinuteBatch and writes it to the store.
//
// Process aggregates are keyed by the ledger's proc_id, resolved through the
// identity cache. A raw NVML PID is not a valid proc_id: the meta database
// reserves 0 for <unattributed> and assigns real ids from a sequence, so
// writing a PID there would produce rows that never join and, after PID
// wraparound, rows attached to an unrelated process.
func flush(ctx context.Context, st *store.Store, acc *accumulator, gpus []gpuState, identities *identityCache) {
	if len(acc.gpus) == 0 && len(acc.procs) == 0 {
		return // nothing to write
	}

	batch := store.MinuteBatch{}

	for gpuIndex, ga := range acc.gpus {
		gpuID, ok := identities.gpuID(gpuIndex)
		if !ok {
			// Without a registered device there is no valid gpu_id to
			// reference, so the sample cannot be stored.
			continue
		}
		s := store.GPUSample{
			GPUID:       gpuID,
			SampleCount: ga.count,
		}
		if ga.hasUtil {
			s.UtilGPUSum = &ga.utilGPUSum
			s.UtilGPUMax = &ga.utilGPUMax
			s.UtilMemSum = &ga.utilMemSum
		}
		if ga.hasTemp {
			s.TempMax = &ga.tempMax
		}
		if ga.hasPower {
			s.PowerMWSum = &ga.powerMWSum
			s.PowerMWMax = &ga.powerMWMax
		}
		if ga.hasVRAMUsed {
			s.VRAMUsedMax = &ga.vramUsedMax
		}
		if ga.hasSMClock {
			s.SMClockMin = &ga.smClockMin
		}
		if ga.hasMemClock {
			s.MemClockMin = &ga.memClockMin
		}
		if ga.hasLimit {
			s.PowerLimitMW = &ga.powerLimit
		}
		if ga.hasThrottle {
			s.ThrottleOR = &ga.throttleOR
		}
		if ga.hasECC {
			s.ECCErrors = &ga.eccErrors
		}
		batch.GPUSamples = append(batch.GPUSamples, s)
	}

	// Resolve every process to a ledger proc_id before writing, so the batch
	// is built from stable keys rather than recycled PIDs.
	type resolved struct {
		procID int
		pa     *procAccum
	}
	resolvedByDevice := make(map[int][]resolved, len(gpus))
	for key, pa := range acc.procs {
		procID, err := identities.resolve(ctx, st, key.pid, key.gpuIndex)
		if err != nil {
			// The process exited between sampling and flushing, so /proc no
			// longer describes it. Fall back to the reserved unattributed
			// row rather than risk attributing its data to a recycled PID.
			log.Printf("[collector] PID %d on GPU %d could not be resolved: %v", key.pid, key.gpuIndex, err)
			procID = unattributedProcID
		}
		resolvedByDevice[key.gpuIndex] = append(resolvedByDevice[key.gpuIndex], resolved{procID: int(procID), pa: pa})
	}

	// Several unresolved processes share the reserved row, so fold them into
	// one aggregate per device to keep the (ts, proc_id, gpu_id) key unique.
	merged := make(map[procKey]*procAccum, len(acc.procs))
	for gpuIndex, entries := range resolvedByDevice {
		for _, entry := range entries {
			key := procKey{pid: entry.procID, gpuIndex: gpuIndex}
			merged[key] = mergeProcAccum(merged, key, entry.pa)
		}
	}

	for key, pa := range merged {
		gpuID, ok := identities.gpuID(key.gpuIndex)
		if !ok {
			continue
		}
		agg := store.ProcessAggregate{
			ProcessID: int64(key.pid),
			GPUID:     gpuID,
		}
		if pa.hasVRAM {
			agg.VRAMUsedBytes = &pa.vramMax
		}
		batch.Aggregates = append(batch.Aggregates, agg)
	}

	if err := st.WriteMinute(ctx, batch); err != nil {
		log.Printf("[collector] Failed to write minute batch: %v", err)
	} else {
		gpuCount := len(batch.GPUSamples)
		procCount := len(batch.Aggregates)
		totalSamples := int64(0)
		for _, s := range batch.GPUSamples {
			totalSamples += s.SampleCount
		}
		log.Printf("[collector] Flushed: %d GPU(s), %d sample(s), %d process(es)",
			gpuCount, totalSamples, procCount)
	}
}
