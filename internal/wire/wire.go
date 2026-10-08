// Package wire defines the shared socket message types used by the daemon
// and clients. It has no dependencies on eBPF, NVML, or database code.
package wire

import (
	"encoding/json"
	"time"
)

// Envelope is the JSON envelope sent over the Unix socket.
// It wraps all message types with a type discriminator and timestamp.
type Envelope struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp_unix_nano"`
	Data      json.RawMessage `json:"data"`
}

// Encode serialises a typed payload into an Envelope JSON line.
func Encode(msgType string, ts int64, data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{
		Type:      msgType,
		Timestamp: ts,
		Data:      raw,
	})
}

// Decode parses a JSON line into an Envelope.
func Decode(line []byte) (Envelope, error) {
	var env Envelope
	err := json.Unmarshal(line, &env)
	return env, err
}

// Message type constants
const (
	TypeGPU   = "gpu"
	TypeProcs = "procs"
	TypeXid   = "xid"
	TypeAgg   = "agg"
	TypeEvent = "event"
)

// GPUSample represents a snapshot of GPU telemetry for one device.
// JSON fields use PascalCase to match the frontend's expectations.
type GPUSample struct {
	TsNano         int64  `json:"TsNano"`
	UUID           string `json:"UUID"`
	Index          uint   `json:"Index"`
	UtilGPU        uint   `json:"UtilGPU"`
	UtilMem        uint   `json:"UtilMem"`
	MemUsed        uint64 `json:"MemUsed"`
	TempC          uint   `json:"TempC"`
	PowerMW        uint   `json:"PowerMW"`
	PowerLimitMW   uint   `json:"PowerLimitMW"`
	SMClockMHz     uint   `json:"SMClockMHz"`
	MemClockMHz    uint   `json:"MemClockMHz"`
	ThrottleReason uint64 `json:"ThrottleReason"`
	ECCErrors      uint64 `json:"ECCErrors"`
	ValidFields    uint64 `json:"ValidFields"`
	Valid          bool   `json:"Valid"`
}

// ProcessSample represents per-process GPU usage.
type ProcessSample struct {
	PID       uint   `json:"PID"`
	GPUUUID   string `json:"GPUUUID"`
	VRAMBytes uint64 `json:"VRAMBytes"`
	VRAMValid bool   `json:"VRAMValid"`
}

// ProcList bundles all per-process entries with completion status.
type ProcList struct {
	TsNano   int64           `json:"TsNano"`
	Procs    []ProcessSample `json:"Procs"`
	Complete bool            `json:"Complete"`
}

// Xid represents an Xid event from the NVIDIA driver.
type Xid struct {
	TsNano int64  `json:"TsNano"`
	UUID   string `json:"UUID"`
	Index  uint   `json:"Index"`
	Code   uint64 `json:"Code"`
}

// AggRow represents aggregated CUDA operation counts from eBPF.
type AggRow struct {
	TsNano       int64  `json:"TsNano"`
	Tgid         uint32 `json:"Tgid"`
	Ordinal      uint32 `json:"Ordinal"`
	ApiID        uint32 `json:"ApiID"`
	Count        uint64 `json:"Count"`
	Bytes        uint64 `json:"Bytes"`
	LatencySumNs uint64 `json:"LatencySumNs"`
	LatencyMaxNs uint64 `json:"LatencyMaxNs"`
	AllocBytes   uint64 `json:"AllocBytes"`
	FreeBytes    uint64 `json:"FreeBytes"`
	Errors       uint64 `json:"Errors"`
	UvmFaults    uint64 `json:"UvmFaults"`
	UvmEvicts    uint64 `json:"UvmEvicts"`
}

// Event represents a raw eBPF event.
type Event struct {
	TsNano          uint64 `json:"TsNano"`
	StartBoottimeNs uint64 `json:"StartBoottimeNs"`
	LatencyNs       uint64 `json:"LatencyNs"`
	Address         uint64 `json:"Address"`
	Bytes           uint64 `json:"Bytes"`
	Tgid            uint32 `json:"Tgid"`
	Tid             uint32 `json:"Tid"`
	DeviceOrdinal   uint32 `json:"DeviceOrdinal"`
	ApiID           uint32 `json:"ApiID"`
	Flags           uint32 `json:"Flags"`
	Status          int32  `json:"Status"`
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

// NowUTC returns the current time as unix nanoseconds.
func NowUTC() int64 {
	return time.Now().UnixNano()
}
