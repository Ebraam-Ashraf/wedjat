package collector

import (
	"sync"
	"time"
)

// GPULive is the most recent raw NVML reading for one GPU.
//
// Unlike gpuAccum, which folds samples into sums and extrema for the database,
// this is a single unaggregated sample. It backs the live socket feed, where
// showing the current instant is the point.
type GPULive struct {
	Index       int
	UtilGPU     int
	UtilMem     int
	TempC       int
	PowerMW     int
	VRAMUsed    int64
	SMClockMHz  int
	MemClockMHz int
	Valid       bool
}

// ProcLive is the most recent NVML reading for one (pid, gpu) pair.
type ProcLive struct {
	PID       int
	GPUIndex  int
	VRAMBytes int64
	VRAMValid bool
}

// LiveState is a point-in-time copy of everything the collector knows.
//
// The zero value is valid and means "no sample taken yet"; consumers should
// check Valid on each GPU rather than assuming a non-zero reading.
type LiveState struct {
	// UnixNano is when the sample was taken.
	UnixNano int64
	// MinuteUnix is the UTC minute this state belongs to, matching the ts
	// bucketing used by the daily database.
	MinuteUnix int64
	// GPUs and Procs are sorted by index and pid respectively, so every
	// client sees the same ordering.
	GPUs  []GPULive
	Procs []ProcLive
}

// liveState publishes the most recent sample to readers outside the poll loop.
// The socket server reads it; without this the accumulator is unreachable once
// sample() returns.
type liveState struct {
	mu    sync.RWMutex
	state LiveState
}

func (l *liveState) publish(gpus []GPULive, procs []ProcLive, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state = LiveState{
		UnixNano:   at.UnixNano(),
		MinuteUnix: at.UTC().Truncate(time.Minute).Unix(),
		GPUs:       gpus,
		Procs:      procs,
	}
}

func (l *liveState) read() LiveState {
	l.mu.RLock()
	defer l.mu.RUnlock()
	// The slices are replaced wholesale on every publish, never mutated in
	// place, so handing the header to a reader is safe. The reader must
	// still treat them as read-only.
	return l.state
}

// liveReader is the read side exposed to other stages.
type liveReader interface {
	Live() LiveState
}
