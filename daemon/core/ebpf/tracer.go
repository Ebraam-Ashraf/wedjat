package ebpf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

// The Go mirrors of the eBPF ABI in ebpf/common.h. Field order and width must
// match the C structs exactly.

type bpfEvent struct {
	TsNano          uint64
	StartBoottimeNs uint64
	LatencyNs       uint64
	Address         uint64
	Bytes           uint64
	Tgid            uint32
	Tid             uint32
	DeviceOrdinal   uint32
	ApiID           uint32
	Flags           uint32
	Status          int32
}

// Event IDs, mirroring enum event_id.
const (
	eventLaunch     = 4
	eventAlloc      = 5
	eventFree       = 6
	eventMemcpy     = 7
	eventSync       = 8
	eventUvmFault   = 9
	eventUvmMigrate = 10
	eventUvmEvict   = 11
	eventIoctl      = 12
	eventMmap       = 13
	eventProcExec   = 16
	eventProcExit   = 17
	eventUvmIoctl   = 18

	// eventFlagDeviceUnknown marks an event whose device could not be resolved.
	eventFlagDeviceUnknown = 1 << 0
)

// unknownDevice is the sentinel the kernel uses when it could not resolve a
// CUDA-visible ordinal. It does not name a device.
const unknownDevice = 0xffffffff

type aggKey struct {
	Tgid          uint32
	DeviceOrdinal uint32
	ApiID         uint32
	Pad           uint32
}

type aggVal struct {
	Count        uint64
	Bytes        uint64
	LatencySumNs uint64
	LatencyMaxNs uint64
	AllocBytes   uint64
	FreeBytes    uint64
	Errors       uint64
	UvmFaults    uint64
	UvmEvicts    uint64
}

type inflightKey struct {
	PidTgid uint64
	ApiID   uint32
	Pad     uint32
}

type inflightVal struct {
	StartTsNs     uint64
	Arg1          uint64
	Arg2          uint64
	DeviceOrdinal uint32
	Flags         uint32
}

type statsVal struct {
	RingbufDrops        uint64
	MapUpdateFailures   uint64
	UnknownDeviceEvents uint64
	AllocFreeMisses     uint64
}

type configVal struct {
	Flags       uint32
	SyncStallUs uint32
}

// configFlagRawCapture mirrors CONFIG_F_RAW_CAPTURE. With it set the kernel
// sends every event instead of only counting them.
const configFlagRawCapture = 1 << 0

// Tracer owns the loaded eBPF collection and everything attached to it.
type Tracer struct {
	collection *ebpf.Collection
	links      []link.Link
	aggMap     *ebpf.Map
	statsMap   *ebpf.Map
	inflight   *ebpf.Map
	reader     *ringbuf.Reader

	// sections maps a program key to the ELF section it came from. The loaded
	// spec does not carry it, and the section is what decides how to attach.
	sections map[string]string

	// Attached and Failed make a partial load visible instead of silently
	// reporting less data than the operator expects.
	Attached []string
	Failed   []string

	closeOnce sync.Once
}

// Close releases every link and map. Pinned state maps are left in place on
// purpose: they are meant to survive the daemon.
func (t *Tracer) Close() error {
	t.closeOnce.Do(func() {
		if t.reader != nil {
			t.reader.Close()
		}
		for _, l := range t.links {
			l.Close()
		}
		if t.collection != nil {
			t.collection.Close()
		}
	})
	return nil
}

// SetConfig writes the daemon's tracing configuration into the kernel side.
func (t *Tracer) SetConfig(rawCapture bool, syncStallUs uint32) error {
	m, ok := t.collection.Maps["config_map"]
	if !ok {
		return errors.New("tracer: config_map missing")
	}
	var flags uint32
	if rawCapture {
		flags |= configFlagRawCapture
	}
	return m.Put(uint32(0), configVal{Flags: flags, SyncStallUs: syncStallUs})
}

// Stats mirrors the kernel's self-reported counters.
type Stats struct {
	RingbufDrops        uint64
	MapUpdateFailures   uint64
	UnknownDeviceEvents uint64
	AllocFreeMisses     uint64
	// Unattributed counts events userspace could not attribute to a GPU,
	// because no CUDA context was available to name the device. The daemon's own
	// NVML traffic causes these, so they are expected rather than an error, but
	// they are also data that is never stored.
	Unattributed uint64
}

// statsSlots is EVENT_ID_MAX, the length of the stats array.
const statsSlots = 20

// Stats sums the kernel's counters. Non-zero ring buffer drops mean userspace
// is not draining events fast enough.
func (t *Tracer) Stats() (Stats, error) {
	if t.statsMap == nil {
		return Stats{}, errors.New("tracer: stats_map missing")
	}
	var out Stats
	for slot := uint32(0); slot < statsSlots; slot++ {
		var perCPU []statsVal
		if err := t.statsMap.Lookup(slot, &perCPU); err != nil {
			continue
		}
		for _, v := range perCPU {
			out.RingbufDrops += v.RingbufDrops
			out.MapUpdateFailures += v.MapUpdateFailures
			out.UnknownDeviceEvents += v.UnknownDeviceEvents
			out.AllocFreeMisses += v.AllocFreeMisses
		}
	}
	out.Unattributed = uint64(unattributed.Load())
	return out, nil
}

// decodeEvent reads the fixed 64-byte event record.
func decodeEvent(raw []byte) (bpfEvent, error) {
	if len(raw) < 64 {
		return bpfEvent{}, fmt.Errorf("short event record: %d bytes", len(raw))
	}
	return bpfEvent{
		TsNano:          binary.LittleEndian.Uint64(raw[0:8]),
		StartBoottimeNs: binary.LittleEndian.Uint64(raw[8:16]),
		LatencyNs:       binary.LittleEndian.Uint64(raw[16:24]),
		Address:         binary.LittleEndian.Uint64(raw[24:32]),
		Bytes:           binary.LittleEndian.Uint64(raw[32:40]),
		Tgid:            binary.LittleEndian.Uint32(raw[40:44]),
		Tid:             binary.LittleEndian.Uint32(raw[44:48]),
		DeviceOrdinal:   binary.LittleEndian.Uint32(raw[48:52]),
		ApiID:           binary.LittleEndian.Uint32(raw[52:56]),
		Flags:           binary.LittleEndian.Uint32(raw[56:60]),
		Status:          int32(binary.LittleEndian.Uint32(raw[60:64])),
	}, nil
}

// tracerTickInterval is how often the kernel counters are drained. agg_map is
// bounded, so a slow drain eventually fills it.
const tracerTickInterval = time.Second
