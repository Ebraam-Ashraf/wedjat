package ebpf

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
	"github.com/cilium/ebpf/ringbuf"
	"golang.org/x/sys/unix"
)

// defaultSyncStallUs is how long a single CUDA sync may take before it is
// recorded as a stall. Without raw capture the kernel only reports that a sync
// finished, so this is judged from the latency it measured, not by watching the
// process.
const defaultSyncStallUs = 250_000

// hungSyncUs is how long a sync may stay in flight before it is reported as a
// hang. It is far above a stall because an inflight entry that old is no longer
// slow work, it is a process blocked inside the driver.
const hungSyncUs = 2_000_000

// consumeEvents drains the ring buffer until the context is cancelled.
//
// This must run continuously rather than once per tick: the ring buffer has a
// fixed size, and if userspace falls behind the kernel drops the oldest records
// and counts them in stats_map. Nothing here is allowed to block, so every
// record is handled and the loop only ever waits on the reader itself.
func (t *Tracer) consumeEvents(ctx context.Context, database *db.DB, ids *tracerIdentity) {
	if t.reader == nil {
		return
	}
	for {
		record, err := t.reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) || ctx.Err() != nil {
				return
			}
			log.Printf("tracer: read event: %v", err)
			continue
		}

		event, err := decodeEvent(record.RawSample)
		if err != nil {
			// A malformed record means the ABI drifted. Counting it and
			// carrying on keeps the rest of the stream usable instead of
			// discarding every record behind it.
			malformed.Add(1)
			continue
		}
		t.handleEvent(ctx, database, ids, event)
	}
}

// handleEvent reacts to the events that carry meaning beyond the counters:
// process lifetime, and syncs slow enough to be worth reporting on their own.
func (t *Tracer) handleEvent(ctx context.Context, database *db.DB, ids *tracerIdentity, event bpfEvent) {
	switch event.ApiID {
	case eventProcExec:
		// Register now rather than at the next drain, so a process that runs
		// briefly is still identified before its counters are folded in.
		if _, err := ids.processID(ctx, database, event.Tgid); err != nil {
			log.Printf("tracer: exec of tgid %d could not be identified: %v", event.Tgid, err)
		}

	case eventProcExit:
		// Resolve before forgetting the cache, since closing the row needs
		// the id. A process the tracer never identified has no row to close.
		if procID, err := ids.processID(ctx, database, event.Tgid); err == nil {
			if _, err := database.EndProcess(ctx, procID, int64(event.TsNano/1e9), "exit"); err != nil {
				log.Printf("tracer: close process %d: %v", event.Tgid, err)
			}
		}
		// Drop the cached row so a recycled PID resolves to a new process
		// instead of inheriting the old one's row.
		ids.forget(event.Tgid)

	case eventSync:
		t.reportSyncStall(ctx, database, ids, event)
	}
}

// reportSyncStall records a sync that took longer than the configured
// threshold. Repeats within the incident dedupe window collapse into one row
// with a rising occurrence count instead of filling the table.
func (t *Tracer) reportSyncStall(ctx context.Context, database *db.DB, ids *tracerIdentity, event bpfEvent) {
	thresholdNs := uint64(t.syncStallUs()) * 1000
	if thresholdNs == 0 || event.LatencyNs < thresholdNs {
		return
	}

	procID, err := ids.processID(ctx, database, event.Tgid)
	if err != nil {
		return
	}
	// An unresolvable device is stored as NULL rather than as zero, so an
	// incident is never pinned to whatever GPU happens to have id 0.
	var gpu *int64
	if gpuID, ok := ids.gpuID(event.DeviceOrdinal); ok {
		gpu = &gpuID
	}

	seconds := int64(event.TsNano / 1e9)
	if _, err := database.WriteIncident(ctx, db.Incident{
		Type:      db.IncidentSyncStall,
		ProcessID: &procID,
		GPUID:     gpu,
		FirstTS:   seconds,
		LastTS:    seconds,
		DedupeKey: fmt.Sprintf("sync_stall:%d:%s", procID, deviceKey(event.DeviceOrdinal)),
		Summary:   fmt.Sprintf("CUDA sync stalled for %d ms", event.LatencyNs/1e6),
		Detail:    fmt.Sprintf("api_id=%d tid=%d device_ordinal=%d latency_ns=%d", event.ApiID, event.Tid, event.DeviceOrdinal, event.LatencyNs),
	}); err != nil {
		log.Printf("tracer: write sync stall incident: %v", err)
	}
}

// deviceKey names a device for a dedupe key. An ordinal the kernel could not
// resolve still has to produce a stable key, so it is spelled out rather than
// folded into "unknown" alongside every other unattributed stall.
func deviceKey(ordinal uint32) string {
	if ordinal == unknownDevice {
		return "unknown"
	}
	return strconv.FormatUint(uint64(ordinal), 10)
}

// scanHungSyncs reports syncs that are still in flight long after any
// legitimate GPU work could take.
//
// The kernel's inflight map holds an entry for the whole duration of a call, so
// an entry that old means the thread has not come back. This is the one place
// a wedged process can be caught, since a process that never returns also never
// emits the event that would have closed its counters.
func (t *Tracer) scanHungSyncs(ctx context.Context, database *db.DB, ids *tracerIdentity) error {
	if t.inflight == nil {
		return errors.New("tracer: cuda_inflight_map missing")
	}

	now, err := boottimeNs()
	if err != nil {
		return err
	}
	threshold := uint64(hungSyncUs) * 1000

	iterator := t.inflight.Iterate()
	var key inflightKey
	var perCPU []inflightVal
	for iterator.Next(&key, &perCPU) {
		tgid := uint32(key.PidTgid >> 32)
		if key.ApiID != eventSync {
			continue
		}

		// The entry is per thread, so only the CPU that owns it has a start
		// time; the rest are zero and must not be read as infinitely old.
		var start uint64
		for _, v := range perCPU {
			if v.StartTsNs > start {
				start = v.StartTsNs
			}
		}
		if start == 0 || now < start || now-start < threshold {
			continue
		}

		procID, err := ids.processID(ctx, database, tgid)
		if err != nil {
			continue
		}
		stalledMs := (now - start) / 1e6
		if _, err := database.WriteIncident(ctx, db.Incident{
			Type:      db.IncidentSyncHang,
			ProcessID: &procID,
			FirstTS:   int64(start / 1e9),
			LastTS:    int64(now / 1e9),
			DedupeKey: fmt.Sprintf("sync_hang:%d", procID),
			Summary:   fmt.Sprintf("CUDA sync in flight for %d ms", stalledMs),
			Detail:    fmt.Sprintf("tid=%d api_id=%d", uint32(key.PidTgid), key.ApiID),
		}); err != nil {
			log.Printf("tracer: write sync hang incident: %v", err)
		}
	}
	return iterator.Err()
}

// syncStallUs returns the configured stall threshold, falling back to the
// default when the kernel map has no configuration written yet.
func (t *Tracer) syncStallUs() uint32 {
	m, ok := t.collection.Maps["config_map"]
	if !ok {
		return defaultSyncStallUs
	}
	var cfg configVal
	if err := m.Lookup(uint32(0), &cfg); err != nil || cfg.SyncStallUs == 0 {
		return defaultSyncStallUs
	}
	return cfg.SyncStallUs
}

// boottimeNs reads the same clock the kernel stamps events with, so an
// in-flight entry's start time can be aged correctly.
func boottimeNs() (uint64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0, fmt.Errorf("read boottime: %w", err)
	}
	return uint64(ts.Sec)*1e9 + uint64(ts.Nsec), nil
}

// malformed counts records that could not be decoded. A non-zero value means
// the built BPF objects and this binary disagree about the event layout.
var malformed atomic.Uint64
