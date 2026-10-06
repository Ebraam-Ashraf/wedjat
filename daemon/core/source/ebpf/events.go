package ebpf

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/ringbuf"
	"golang.org/x/sys/unix"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// defaultSyncStallUs is how long a single CUDA sync may take before it is
// considered a stall.
const defaultSyncStallUs = 250_000

// hungSyncUs is how long a sync may stay in flight before it is reported as a
// hang.
const hungSyncUs = 2_000_000

// consumeEvents drains the ring buffer until the context is cancelled, routing
// events to the db and sock channels.
//
// This must run continuously rather than once per tick: the ring buffer has a
// fixed size, and if userspace falls behind the kernel drops the oldest records
// and counts them in stats_map. Nothing here is allowed to block, so every
// record is handled and the loop only ever waits on the reader itself.
func (s *Session) consumeEvents(ctx context.Context) {
	if s.tracer == nil || s.tracer.reader == nil {
		return
	}

	for {
		record, err := s.tracer.reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) || ctx.Err() != nil {
				return
			}
			log.Printf("eBPF: read event: %v", err)
			continue
		}

		event, err := decodeEvent(record.RawSample)
		if err != nil {
			// A malformed record means the ABI drifted. Count and carry on.
			malformed.Add(1)
			continue
		}

		// On process exit, clear BPF per-process kernel state. This must happen
		// before the process identity is gone from /proc.
		if event.ApiID == eventProcExit {
			if err := s.tracer.clearProcessState(event.Tgid, event.StartBoottimeNs); err != nil {
				log.Printf("eBPF: clear process state for tgid %d: %v", event.Tgid, err)
			}
		}

		// Convert bpfEvent → source.Event and forward to both channels.
		se := source.Event{
			TsNano:          event.TsNano,
			StartBoottimeNs: event.StartBoottimeNs,
			LatencyNs:       event.LatencyNs,
			Address:         event.Address,
			Bytes:           event.Bytes,
			Tgid:            event.Tgid,
			Tid:             event.Tid,
			DeviceOrdinal:   event.DeviceOrdinal,
			ApiID:           event.ApiID,
			Flags:           event.Flags,
			Status:          event.Status,
		}

		source.Send(s.db.Event, se)
		if source.Clients.Load() > 0 {
			source.Send(s.sock.Event, se)
		}
	}
}

// scanHungSyncs scans the inflight map for SYNC entries older than 2 s and
// sends a source.Event with FlagHungSync set for each one found.
func (s *Session) scanHungSyncs(ctx context.Context) {
	if s.tracer == nil || s.tracer.inflight == nil {
		return
	}

	now, err := boottimeNs()
	if err != nil {
		return
	}
	threshold := uint64(hungSyncUs) * 1000

	iterator := s.tracer.inflight.Iterate()
	var key inflightKey
	var perCPU []inflightVal
	for iterator.Next(&key, &perCPU) {
		if key.ApiID != eventSync {
			continue
		}
		tgid := uint32(key.PidTgid >> 32)
		tid := uint32(key.PidTgid)

		// Find the earliest non-zero start timestamp across CPU slots.
		var start uint64
		for _, v := range perCPU {
			if v.StartTsNs == 0 {
				continue
			}
			if start == 0 || v.StartTsNs < start {
				start = v.StartTsNs
			}
		}
		if start == 0 || now < start || now-start < threshold {
			continue
		}

		se := source.Event{
			TsNano:    now,
			Tgid:      tgid,
			Tid:       tid,
			ApiID:     eventSync,
			Flags:     source.FlagHungSync,
			LatencyNs: now - start,
		}
		source.Send(s.db.Event, se)
		if source.Clients.Load() > 0 {
			source.Send(s.sock.Event, se)
		}
	}
}

// stateBelongsToProcess reports whether a map entry's start_boottime_ns was
// written by the same process generation as the exit event. It fails closed:
// when either timestamp is zero the entry is treated as belonging to the
// exiting process and will be deleted, because an entry with no generation
// token can never be attributed to any other process.
func stateBelongsToProcess(tgid uint32, ownerStart, eventStart uint64) bool {
	if tgid == 0 || ownerStart == 0 {
		return false
	}
	return ownerStart == eventStart
}

// clearProcessState removes only state from the process generation named by
// the exit event. A PID may already have been reused by the time userspace
// handles that event, so matching by TGID alone could erase the new process.
func (t *Tracer) clearProcessState(tgid uint32, startBoottimeNs uint64) error {
	if tgid == 0 || startBoottimeNs == 0 {
		return errors.New("missing process generation in exit event")
	}

	if m := t.collection.Maps["tid_to_device"]; m != nil {
		for {
			removed := false
			it := m.Iterate()
			var key threadKey
			var value deviceBinding
			for it.Next(&key, &value) {
				if uint32(key.PidTgid>>32) != tgid ||
					!stateBelongsToProcess(tgid, value.StartBoottimeNs, startBoottimeNs) {
					continue
				}
				if err := m.Delete(&key); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
					return fmt.Errorf("delete tid_to_device: %w", err)
				}
				removed = true
			}
			if err := it.Err(); err != nil {
				return fmt.Errorf("iterate tid_to_device: %w", err)
			}
			if !removed {
				break
			}
		}
	}

	if m := t.collection.Maps["pid_to_device"]; m != nil {
		key := tgid
		var value deviceBinding
		if err := m.Lookup(key, &value); err == nil &&
			stateBelongsToProcess(tgid, value.StartBoottimeNs, startBoottimeNs) {
			if err := m.Delete(key); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
				return fmt.Errorf("delete pid_to_device: %w", err)
			}
		} else if err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
			return fmt.Errorf("lookup pid_to_device: %w", err)
		}
	}

	if m := t.collection.Maps["ctx_to_device"]; m != nil {
		for {
			removed := false
			it := m.Iterate()
			var key ctxKey
			var value deviceBinding
			for it.Next(&key, &value) {
				if key.Tgid != tgid ||
					!stateBelongsToProcess(tgid, value.StartBoottimeNs, startBoottimeNs) {
					continue
				}
				if err := m.Delete(&key); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
					return fmt.Errorf("delete ctx_to_device: %w", err)
				}
				removed = true
			}
			if err := it.Err(); err != nil {
				return fmt.Errorf("iterate ctx_to_device: %w", err)
			}
			if !removed {
				break
			}
		}
	}

	if m := t.collection.Maps["alloc_map"]; m != nil {
		for {
			removed := false
			it := m.Iterate()
			var key allocKey
			var value allocVal
			for it.Next(&key, &value) {
				if key.Tgid != tgid ||
					!stateBelongsToProcess(tgid, value.StartBoottimeNs, startBoottimeNs) {
					continue
				}
				if err := m.Delete(&key); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
					return fmt.Errorf("delete alloc_map: %w", err)
				}
				removed = true
			}
			if err := it.Err(); err != nil {
				return fmt.Errorf("iterate alloc_map: %w", err)
			}
			if !removed {
				break
			}
		}
	}
	return nil
}

// syncStallUs returns the configured stall threshold.
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

// boottimeNs reads CLOCK_BOOTTIME in nanoseconds, the same clock the kernel
// stamps events with.
func boottimeNs() (uint64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0, fmt.Errorf("read boottime: %w", err)
	}
	return uint64(ts.Sec)*1e9 + uint64(ts.Nsec), nil
}
