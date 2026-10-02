package ebpf

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
)

// tracerIdentity turns the kernel's view of a process into the database's.
//
// The kernel knows a process by (tgid, device ordinal). The database knows it
// by row id, and it deliberately does not store a bare tgid as identity because
// the kernel recycles PIDs. The two are bridged here: a tgid is resolved to the
// same process row the NVML path already created, by reusing the process start
// time, so both paths agree on one row per process instance.
//
// A tgid seen only by the tracer and never by NVML is resolved by reading
// /proc itself. If the process has already exited there is nothing to key on,
// and the row is reported as unresolvable rather than attached to a guess.
type tracerIdentity struct {
	mu      sync.Mutex
	bootID  string
	ordinal map[uint32]int64 // device ordinal -> gpu_id
	tgids   map[uint32]int64 // tgid -> proc_id, for this boot
}

func newTracerIdentity(bootID string) *tracerIdentity {
	return &tracerIdentity{
		bootID:  bootID,
		ordinal: map[uint32]int64{},
		tgids:   map[uint32]int64{},
	}
}

// setOrdinal pins the kernel's device ordinal to the database row for that GPU.
// It is resolved once at startup because both sides are stable for the life of
// the process: NVML indexes GPUs and the kernel reports the same index.
func (i *tracerIdentity) setOrdinal(ordinal uint32, gpuID int64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ordinal[ordinal] = gpuID
}

// gpuID resolves a kernel device ordinal. An ordinal the kernel could not
// resolve is reported as unknown rather than mapped to a default device, since
// attributing work to the wrong GPU is worse than not attributing it.
func (i *tracerIdentity) gpuID(ordinal uint32) (int64, bool) {
	if ordinal == unknownDevice {
		return 0, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	id, ok := i.ordinal[ordinal]
	return id, ok
}

// processID resolves a tgid to its process row, consulting the cache first and
// /proc only when the tgid has not been seen this boot.
func (i *tracerIdentity) processID(ctx context.Context, database *db.DB, tgid uint32) (int64, error) {
	if tgid == 0 {
		return 0, fmt.Errorf("tracer: kernel reported tgid 0")
	}

	i.mu.Lock()
	cached, ok := i.tgids[tgid]
	i.mu.Unlock()
	if ok {
		return cached, nil
	}

	// The process start time is what makes this the same row the NVML path
	// uses. Without it a recycled PID would create a second row for a process
	// already recorded, so a missing /proc entry is a miss, not a fallback.
	startTicks, command, err := core.ReadProcessIdentity(uint(tgid))
	if err != nil {
		return 0, err
	}

	procID, err := database.UpsertProcess(ctx, db.ProcessIdentity{
		BootID:        i.bootID,
		TGID:          int64(tgid),
		StartTicks:    startTicks,
		Command:       command,
		FirstSeenUnix: time.Now().Unix(),
	})
	if err != nil {
		return 0, err
	}

	i.mu.Lock()
	i.tgids[tgid] = procID
	i.mu.Unlock()
	return procID, nil
}

// forget drops a tgid from the cache when its process exits, so a recycled PID
// is resolved against the new process's start time rather than the old row.
func (i *tracerIdentity) forget(tgid uint32) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.tgids, tgid)
}
