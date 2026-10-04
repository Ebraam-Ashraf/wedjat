package ebpf

import (
	"errors"
	"time"

	"github.com/cilium/ebpf"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

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

// drainAggregates extracts all entries from the kernel aggregate map and
// returns them as source.AggRow values. It uses LookupAndDelete so each entry
// is consumed atomically — a concurrent kernel update lands in either this
// drain or the next, never both and never neither.
//
// Rows where the merged Count is zero (and all other counters are also zero)
// are skipped to avoid writing empty rows.
func (s *Session) drainAggregates() []source.AggRow {
	if s.tracer == nil || s.tracer.aggMap == nil {
		return nil
	}

	now := time.Now().UnixNano()
	var rows []source.AggRow

	iterator := s.tracer.aggMap.Iterate()
	var key aggKey
	var perCPU []aggVal
	for iterator.Next(&key, &perCPU) {
		// LookupAndDelete atomically removes the entry and returns its value.
		// ErrKeyNotExist means the LRU map evicted the entry between the
		// iterator snapshot and the delete — counts are gone and cannot be
		// recovered.
		if err := s.tracer.aggMap.LookupAndDelete(&key, &perCPU); err != nil {
			if errors.Is(err, ebpf.ErrKeyNotExist) {
				continue
			}
			// Skip on other errors; the next drain will catch remaining entries.
			continue
		}

		merged := mergeAgg(perCPU)
		if merged.Count == 0 && merged.AllocBytes == 0 && merged.FreeBytes == 0 &&
			merged.UvmFaults == 0 && merged.UvmEvicts == 0 && merged.Bytes == 0 {
			continue
		}

		rows = append(rows, source.AggRow{
			TsNano:       now,
			Tgid:         key.Tgid,
			Ordinal:      key.DeviceOrdinal,
			ApiID:        key.ApiID,
			Count:        merged.Count,
			Bytes:        merged.Bytes,
			LatencySumNs: merged.LatencySumNs,
			LatencyMaxNs: merged.LatencyMaxNs,
			AllocBytes:   merged.AllocBytes,
			FreeBytes:    merged.FreeBytes,
			Errors:       merged.Errors,
			UvmFaults:    merged.UvmFaults,
			UvmEvicts:    merged.UvmEvicts,
		})
	}

	return rows
}
