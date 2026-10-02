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
	unresolved := map[aggregateGroup]aggVal{}

	iterator := t.aggMap.Iterate()
	var key aggKey
	var perCPU []aggVal
	for iterator.Next(&key, &perCPU) {
		// LookupAndDelete is what actually takes the value. Next only needs
		// somewhere to put the shape of it, so the lookup below is the read
		// that counts.
		if err := t.aggMap.LookupAndDelete(&key, &perCPU); err != nil {
			if errors.Is(err, ebpf.ErrKeyNotExist) {
				// The kernel updated the row between the iteration and the
				// delete. That counter will be picked up next drain.
				continue
			}
			return fmt.Errorf("drain agg_map: %w", err)
		}

		value := mergeAgg(perCPU)
		if value.Count == 0 && value.AllocBytes == 0 && value.FreeBytes == 0 &&
			value.UvmFaults == 0 && value.UvmEvicts == 0 && value.Bytes == 0 {
			continue
		}

		gk := aggregateGroup{tgid: key.Tgid, ordinal: key.DeviceOrdinal}
		if _, ok := unresolved[gk]; ok {
			continue
		}

		procID, err := ids.processID(ctx, database, key.Tgid)
		if err != nil {
			// A process that exited before we could identify it cannot be
			// attributed. Remember it so the rest of this drain does not retry
			// the same impossible lookup once per API.
			unresolved[gk] = value
			continue
		}
		gpuID, ok := ids.gpuID(key.DeviceOrdinal)
		if !ok {
			unresolved[gk] = value
			continue
		}

		row := merged[gk]
		row.ProcessID = procID
		row.GPUID = gpuID
		applyAPI(&row, key.ApiID, value)
		merged[gk] = row
	}

	if err := iterator.Err(); err != nil {
		return fmt.Errorf("iterate agg_map: %w", err)
	}

	for _, value := range unresolved {
		unattributed.Add(int64(value.Count))
	}

	if len(merged) == 0 {
		return nil
	}
	return database.WriteAggregates(ctx, at, sortedAggregates(merged))
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
