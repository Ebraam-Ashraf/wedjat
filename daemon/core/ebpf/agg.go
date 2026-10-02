package ebpf

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
	"github.com/cilium/ebpf"
)

// aggregateGroup is one process on one device. The kernel keys its counters by
// (tgid, ordinal, api_id); the database keys by (process, device), so the rows
// are merged across every API here before they are written.
type aggregateGroup struct {
	tgid    uint32
	ordinal uint32
}

// mergeAgg collapses the per-CPU values of one aggregate row into a single
// counter set.
//
// Counters add, because each CPU counted its own share of the work. The
// latency maximum takes the largest, not the sum: the slowest single sync is
// the number an operator cares about, and adding the maxima of independent CPUs
// would invent a stall that never happened.
func mergeAgg(values []aggVal) aggVal {
	var out aggVal
	for _, v := range values {
		out.Count += v.Count
		out.Bytes += v.Bytes
		out.LatencySumNs += v.LatencySumNs
		if v.LatencyMaxNs > out.LatencyMaxNs {
			out.LatencyMaxNs = v.LatencyMaxNs
		}
		out.AllocBytes += v.AllocBytes
		out.FreeBytes += v.FreeBytes
		out.Errors += v.Errors
		out.UvmFaults += v.UvmFaults
		out.UvmEvicts += v.UvmEvicts
	}
	return out
}

// drainAggregates takes everything the kernel has counted since the last drain
// and folds it into the database.
//
// Rows are grouped by (process, GPU) because that is what the agg table keys
// on; the per-API rows the kernel keeps are an implementation detail of the hot
// path and are merged here. Keys are removed with a lookup-and-delete so a
// concurrent update either lands in this drain or the next one, never both and
// never neither.
func (t *Tracer) drainAggregates(ctx context.Context, database *db.DB, ids *tracerIdentity, at time.Time) error {
	if t.aggMap == nil {
		return errors.New("tracer: agg_map missing")
	}

	// merged is keyed by (tgid, ordinal) so every API the process used on that
	// device lands in one row.
	merged := map[aggregateGroup]db.Aggregate{}

	// unresolvedCount accumulates the event count for groups whose process or
	// device could not be identified so they can be reported as unattributed.
	var unresolvedCount uint64

	// resolveCache caches the outcome of process/GPU resolution per group so
	// we don't retry an impossible lookup once per API row for the same group.
	type resolvedIDs struct {
		procID int64
		gpuID  int64
		ok     bool
	}
	resolveCache := map[aggregateGroup]resolvedIDs{}

	iterator := t.aggMap.Iterate()
	var key aggKey
	var perCPU []aggVal
	var scanErr error
	for iterator.Next(&key, &perCPU) {
		// LookupAndDelete atomically removes the entry and returns its final
		// value. Next only provides the key shape; the value from Next is
		// discarded in favour of the one returned here.
		//
		// ErrKeyNotExist means the LRU map evicted this entry between the
		// iterator snapshot and the delete — the counts are gone and cannot
		// be recovered. This is distinct from a concurrent writer updating
		// the entry: LookupAndDelete is atomic, so an update that races us
		// either lands in this drain (we see it) or the next one (it
		// survives because the LookupAndDelete finds the updated value).
		if err := t.aggMap.LookupAndDelete(&key, &perCPU); err != nil {
			if errors.Is(err, ebpf.ErrKeyNotExist) {
				continue
			}
			scanErr = fmt.Errorf("drain agg_map: %w", err)
			break
		}

		value := mergeAgg(perCPU)
		if value.Count == 0 && value.AllocBytes == 0 && value.FreeBytes == 0 &&
			value.UvmFaults == 0 && value.UvmEvicts == 0 && value.Bytes == 0 {
			continue
		}

		gk := aggregateGroup{tgid: key.Tgid, ordinal: key.DeviceOrdinal}

		// Check or populate the resolution cache for this group. Each API
		// row for the same (tgid, ordinal) group shares one cache entry so
		// we attempt the /proc lookup at most once per drain per group,
		// but every API row is still accumulated — even when the group is
		// unresolvable its counts are added to unresolvedCount below.
		resolved, cached := resolveCache[gk]
		if !cached {
			procID, procErr := ids.processID(ctx, database, key.Tgid)
			gpuID, gpuOK := ids.gpuID(key.Tgid, key.DeviceOrdinal)
			if procErr != nil || !gpuOK {
				resolveCache[gk] = resolvedIDs{ok: false}
			} else {
				resolveCache[gk] = resolvedIDs{procID: procID, gpuID: gpuID, ok: true}
			}
			resolved = resolveCache[gk]
		}

		if !resolved.ok {
			unresolvedCount += value.Count
			continue
		}

		row := merged[gk]
		row.ProcessID = resolved.procID
		row.GPUID = resolved.gpuID
		applyAPI(&row, key.ApiID, value)
		merged[gk] = row
	}

	if err := iterator.Err(); err != nil {
		scanErr = errors.Join(scanErr, fmt.Errorf("iterate agg_map: %w", err))
	}

	if unresolvedCount > 0 {
		unattributed.Add(int64(unresolvedCount))
	}

	t.backlog.Add(at, sortedAggregates(merged))
	writeErr := t.backlog.Flush(ctx, database.WriteAggregates)
	return errors.Join(scanErr, writeErr)
}

// unattributed counts events the kernel recorded but that cannot be attributed
// to a GPU, because no CUDA context resolved to a device. The daemon's own NVML
// traffic produces these continuously, so they are counted rather than logged:
// one line per drain would bury everything else. They are reported alongside the
// kernel's own unknown-device counter in the heartbeat.
var unattributed atomic.Int64

// applyAPI folds one API's counters into the row for its process and device.
func applyAPI(row *db.Aggregate, apiID uint32, v aggVal) {
	row.Errors += int64(v.Errors)
	row.UvmFaults += int64(v.UvmFaults)
	row.UvmEvicts += int64(v.UvmEvicts)

	switch apiID {
	case eventLaunch:
		row.Launches += int64(v.Count)
	case eventMemcpy:
		row.MemcpyCalls += int64(v.Count)
		row.MemcpyBytes += int64(v.Bytes)
	case eventAlloc:
		row.AllocCalls += int64(v.Count)
		row.AllocBytes += int64(v.AllocBytes)
	case eventFree:
		row.FreeBytes += int64(v.FreeBytes)
	case eventSync:
		row.SyncCalls += int64(v.Count)
		row.SyncUsSum += int64(v.LatencySumNs / 1000)
		row.SyncUsMax = maxInt64(int64(v.LatencyMaxNs/1000), row.SyncUsMax)
	case eventIoctl:
		row.IoctlCalls += int64(v.Count)
	}
}

// sortedAggregates returns the rows ordered by process then device, so a batch
// always writes in the same order regardless of map iteration order.
func sortedAggregates(in map[aggregateGroup]db.Aggregate) []db.Aggregate {
	out := make([]db.Aggregate, 0, len(in))
	for _, value := range in {
		out = append(out, value)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].ProcessID != out[b].ProcessID {
			return out[a].ProcessID < out[b].ProcessID
		}
		return out[a].GPUID < out[b].GPUID
	})
	return out
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
