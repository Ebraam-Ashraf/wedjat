package collector

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/store"
)

// ProcIdentity is the stable identity of one process instance.
//
// A bare PID is not an identity: the kernel recycles PIDs, so the same number
// can refer to a different program moments after it exits. StartTicks, the
// process start time in clock ticks since boot, changes with every execution,
// which is what makes the pair stable. This mirrors the store's
// (boot_id, tgid, start_ticks) uniqueness constraint.
type ProcIdentity struct {
	BootID     string
	TGID       int64
	StartTicks int64
	Command    string
	Cmdline    string
}

// identityCache resolves NVML-reported PIDs into ledger proc_ids.
//
// Resolution is cached because a process is seen on many polls before a flush,
// and re-reading /proc plus rewriting the procs table on every 2-second tick
// would be wasteful. The cache is keyed by PID and validated against
// StartTicks, so a reused PID is detected and re-resolved rather than
// inheriting the previous process's identity.
type identityCache struct {
	mu sync.Mutex
	// byProc maps a full identity to its ledger proc_id.
	byProc map[ProcIdentity]int64
	// byPID maps a live PID to the identity last seen under it.
	byPID map[int]ProcIdentity
	// gpuByProc maps proc_id to the NVML device index it was last seen on.
	gpuByProc map[int64]int
	// nextOrdinal hands out the next dense per-process device ordinal.
	nextOrdinal map[int64]int
	// gpuIDs maps NVML device index to the store's gpus.gpu_id.
	gpuIDs map[int]int64
}

func newIdentityCache() *identityCache {
	return &identityCache{
		byProc:      make(map[ProcIdentity]int64),
		byPID:       make(map[int]ProcIdentity),
		gpuByProc:   make(map[int64]int),
		nextOrdinal: make(map[int64]int),
		gpuIDs:      make(map[int]int64),
	}
}

// setGPUID records the store gpu_id for an NVML device index, as returned by
// UpsertGPU. Per-minute rows must reference the ledger key, not the NVML index.
func (c *identityCache) setGPUID(index int, gpuID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gpuIDs[index] = gpuID
}

func (c *identityCache) gpuID(index int) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	gpuID, ok := c.gpuIDs[index]
	return gpuID, ok
}

// resolve returns the ledger proc_id for a PID, registering it on first sight.
//
// An error means the process could not be read or the store rejected the
// write. A process that exits between the NVML sample and this call is no
// longer in /proc, so the caller falls back to the reserved unattributed row:
// attributing its data to a recycled PID would be worse than not attributing
// it at all.
func (c *identityCache) resolve(ctx context.Context, st *store.Store, pid, gpuIndex int) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("collector: invalid pid %d", pid)
	}

	identity, cached := c.cached(pid)
	if cached {
		// Trust the cache only while the PID still refers to the same
		// process instance.
		current, err := ReadProcIdentity(pid)
		if err == nil && current.StartTicks == identity.StartTicks {
			if err := c.mapDevice(ctx, st, identity, gpuIndex); err != nil {
				return c.procIDOf(identity), err
			}
			return c.procIDOf(identity), nil
		}
		// The PID was reused. Drop the stale PID entry and re-resolve. The
		// byProc entry stays, since the previous instance is still a real
		// ledger row and the lifecycle prober may need to close it.
		c.forgetPID(pid, identity)
	}

	identity, err := ReadProcIdentity(pid)
	if err != nil {
		return 0, err
	}

	procID, err := c.ensureProcess(ctx, st, identity)
	if err != nil {
		return 0, err
	}
	if err := c.mapDevice(ctx, st, identity, gpuIndex); err != nil {
		return procID, err
	}
	return procID, nil
}

func (c *identityCache) cached(pid int) (ProcIdentity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	identity, ok := c.byPID[pid]
	return identity, ok
}

func (c *identityCache) procIDOf(identity ProcIdentity) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byProc[identity]
}

func (c *identityCache) forgetPID(pid int, identity ProcIdentity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.byPID[pid]; ok && current == identity {
		delete(c.byPID, pid)
	}
}

// ensureProcess returns the ledger proc_id for an identity, inserting the
// procs row on first sight.
func (c *identityCache) ensureProcess(ctx context.Context, st *store.Store, identity ProcIdentity) (int64, error) {
	c.mu.Lock()
	procID, seen := c.byProc[identity]
	c.mu.Unlock()
	if seen {
		return procID, nil
	}

	procID, err := st.UpsertProcess(ctx, store.ProcessIdentity{
		BootID:      identity.BootID,
		TGID:        identity.TGID,
		StartTicks:  identity.StartTicks,
		Command:     identity.Command,
		Cmdline:     identity.Cmdline,
		FirstSeenAt: time.Now().UTC().Unix(),
	})
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	c.byProc[identity] = procID
	c.byPID[int(identity.TGID)] = identity
	c.mu.Unlock()
	return procID, nil
}

// mapDevice records that a process is using a GPU, writing proc_devices once
// per (process, device) pair.
func (c *identityCache) mapDevice(ctx context.Context, st *store.Store, identity ProcIdentity, gpuIndex int) error {
	procID := c.procIDOf(identity)
	if procID == 0 {
		return nil
	}
	gpuID, ok := c.gpuID(gpuIndex)
	if !ok || gpuID == 0 {
		// The GPU was never registered, so there is no valid foreign key to
		// reference. The per-minute aggregate is still written.
		return nil
	}

	c.mu.Lock()
	mappedIndex, wasMapped := c.gpuByProc[procID]
	ordinal, hasOrdinal := c.nextOrdinal[procID]
	if !wasMapped {
		// First device for this process: ordinal 0. Further devices get
		// successive ordinals, which is all proc_devices requires.
		ordinal = 0
	}
	c.mu.Unlock()

	if wasMapped && mappedIndex == gpuIndex {
		return nil
	}
	if hasOrdinal && wasMapped {
		ordinal = c.takeOrdinal(procID)
	}

	if err := st.MapProcessDevice(ctx, procID, int64(ordinal), gpuID); err != nil {
		return err
	}

	c.mu.Lock()
	c.gpuByProc[procID] = gpuIndex
	c.nextOrdinal[procID] = ordinal + 1
	c.mu.Unlock()
	return nil
}

func (c *identityCache) takeOrdinal(procID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextOrdinal[procID]
}

// ReadProcIdentity reads a process's stable identity from /proc.
//
// Field 22 of /proc/<pid>/stat is starttime. The comm field (field 2) is
// parenthesised and may itself contain spaces and parentheses, so the fields
// after it are located from the LAST closing parenthesis rather than by naive
// whitespace splitting.
//
// Exported so integration tests can register a real process through the same
// path the collector uses.
func ReadProcIdentity(pid int) (ProcIdentity, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcIdentity{}, err
	}

	open := bytes.IndexByte(data, '(')
	end := bytes.LastIndexByte(data, ')')
	if open < 0 || end < open || end+1 >= len(data) {
		return ProcIdentity{}, fmt.Errorf("collector: malformed /proc/%d/stat", pid)
	}

	identity := ProcIdentity{
		TGID:    int64(pid),
		Command: string(data[open+1 : end]),
	}

	// After the closing ')' comes state, then ppid. Whitespace-splitting this
	// tail puts field 3 at index 0, so field 22 (starttime) is index 19.
	fields := strings.Fields(string(data[end+1:]))
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return ProcIdentity{}, fmt.Errorf("collector: /proc/%d/stat has %d fields after comm", pid, len(fields))
	}
	startTicks, err := strconv.ParseInt(fields[startTimeIndex], 10, 64)
	if err != nil {
		return ProcIdentity{}, fmt.Errorf("collector: parse starttime for pid %d: %w", pid, err)
	}
	identity.StartTicks = startTicks

	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		// Arguments are NUL separated; join with spaces for display.
		identity.Cmdline = strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
	}
	if bootID, err := store.ReadBootID(); err == nil {
		identity.BootID = strings.TrimSpace(bootID)
	}
	return identity, nil
}
