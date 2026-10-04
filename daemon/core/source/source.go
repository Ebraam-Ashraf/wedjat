// Package source defines the shared types and channels used throughout the
// Wedjat daemon. Sources produce typed data to separate channels; consumers
// (DB layer and socket layer) receive from their own channel sets.
package source

import (
	"sync/atomic"
)

// DeviceInfo holds the static identity of a GPU. It is read once at startup
// because these fields do not change while the machine is up.
type DeviceInfo struct {
	Index         uint
	UUID          string
	Name          string
	PCIBusID      string
	DriverVersion string
	VRAMTotal     uint64
	VRAMValid     bool
}

// GPUSample represents a snapshot of GPU telemetry for one device.
type GPUSample struct {
	TsNano         int64
	UUID           string
	Index          uint
	UtilGPU        uint
	UtilMem        uint
	MemUsed        uint64
	TempC          uint
	PowerMW        uint
	PowerLimitMW   uint
	SMClockMHz     uint
	MemClockMHz    uint
	ThrottleReason uint64
	ECCErrors      uint64
	ValidFields    uint64
	Valid          bool
}

// ProcessSample represents per-process GPU usage. GPUUUID attributes the
// process to a specific device; without it multi-GPU hosts cannot be told apart.
type ProcessSample struct {
	PID       uint
	GPUUUID   string
	VRAMBytes uint64
	VRAMValid bool
}

// ProcList bundles all per-process entries with completion status.
type ProcList struct {
	TsNano   int64
	Procs    []ProcessSample
	Complete bool
}

// Xid represents an Xid event from the NVIDIA driver.
type Xid struct {
	TsNano int64
	UUID   string
	Index  uint
	Code   uint64
}

// AggRow represents aggregated CUDA operation counts from eBPF.
type AggRow struct {
	TsNano       int64
	Tgid         uint32
	Ordinal      uint32
	ApiID        uint32
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

// Event represents a raw eBPF event.
type Event struct {
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

// FlagHungSync marks a hung sync event (userspace-only flag).
const FlagHungSync = 1 << 8

// Validity-bit constants. These mirror the DEVICE_VALID_* bits in the C poller.
// A field whose bit is clear was not reported by the driver and must be stored
// as SQL NULL, never as zero.
const (
	ValidGPUUtil        uint64 = 1 << 1
	ValidMemUtil        uint64 = 1 << 2
	ValidMemUsed        uint64 = 1 << 3
	ValidTemp           uint64 = 1 << 6
	ValidPower          uint64 = 1 << 7
	ValidSMClock        uint64 = 1 << 8
	ValidMemClock       uint64 = 1 << 9
	ValidThrottleReason uint64 = 1 << 10
	ValidPowerLimit     uint64 = 1 << 11
	ValidECCUncorrected uint64 = 1 << 13
)

// Chans holds the typed channels that sources send to and consumers receive from.
type Chans struct {
	GPU   chan []GPUSample
	Procs chan ProcList
	Xid   chan Xid
	Agg   chan []AggRow
	Event chan Event
}

// NewChans creates a new set of channels with the specified buffer size.
func NewChans(buf int) Chans {
	return Chans{
		GPU:   make(chan []GPUSample, buf),
		Procs: make(chan ProcList, buf),
		Xid:   make(chan Xid, buf),
		Agg:   make(chan []AggRow, buf),
		Event: make(chan Event, buf),
	}
}

// Global counters for client tracking and dropped messages
var (
	Clients atomic.Int32  // Number of connected socket clients
	Dropped atomic.Uint64 // Count of messages dropped due to full channels
)

// Send sends a value to a channel non-blockingly. If the channel is full,
// the value is dropped and the Dropped counter is incremented.
func Send[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
		Dropped.Add(1)
	}
}
