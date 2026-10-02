package ebpf

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/nvml"
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
	mu             sync.Mutex
	bootID         string
	devices        []gpuIdentity
	ordinalsByTGID map[uint32]ordinalCache
	tgids          map[uint32]processCache
}

type ordinalCache struct {
	startTicks int64
	byOrdinal  map[uint32]int64
}

func (c ordinalCache) matches(startTicks int64) bool {
	return c.startTicks != 0 && c.startTicks == startTicks
}

type processCache struct {
	startTicks int64
	procID     int64
}

type gpuIdentity struct {
	device nvml.DeviceInfo
	id     int64
}

func newTracerIdentity(bootID string) *tracerIdentity {
	return &tracerIdentity{
		bootID:         bootID,
		ordinalsByTGID: map[uint32]ordinalCache{},
		tgids:          map[uint32]processCache{},
	}
}

func (i *tracerIdentity) setDevices(devices []gpuIdentity) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.devices = append([]gpuIdentity(nil), devices...)
}

// gpuID resolves the CUDA-visible ordinal in this process. Device ordering is
// process-specific, so an ambiguous mapping is left unattributed instead of
// guessed from NVML's physical device index.
func (i *tracerIdentity) gpuID(tgid, ordinal uint32) (int64, bool) {
	if ordinal == unknownDevice {
		return 0, false
	}
	i.mu.Lock()
	process, processKnown := i.tgids[tgid]
	cache, cached := i.ordinalsByTGID[tgid]
	devices := append([]gpuIdentity(nil), i.devices...)
	i.mu.Unlock()
	if !processKnown {
		return 0, false
	}
	if cached && cache.matches(process.startTicks) {
		id, ok := cache.byOrdinal[ordinal]
		return id, ok
	}
	env, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(tgid), 10) + "/environ")
	if err != nil {
		return 0, false
	}
	byOrdinal := resolveCUDAOrdinals(env, devices)
	startTicks, _, err := core.ReadProcessIdentity(uint(tgid))
	if err != nil || startTicks != process.startTicks {
		return 0, false
	}
	i.mu.Lock()
	if current, ok := i.tgids[tgid]; !ok || current.startTicks != process.startTicks {
		i.mu.Unlock()
		return 0, false
	}
	i.ordinalsByTGID[tgid] = ordinalCache{startTicks: process.startTicks, byOrdinal: byOrdinal}
	i.mu.Unlock()
	id, ok := byOrdinal[ordinal]
	return id, ok
}

// resolveCUDAOrdinals maps the CUDA-visible ordinal numbers that this process
// sees to their database GPU IDs.
//
// The mapping depends on CUDA_VISIBLE_DEVICES and CUDA_DEVICE_ORDER in the
// process environment. UUID tokens (GPU-…) resolve exactly against the known
// device list. Integer tokens only resolve unambiguously when combined with
// PCI_BUS_ID ordering or when there is exactly one device. An empty
// CUDA_VISIBLE_DEVICES means no devices are visible and the map is empty.
// Ambiguous cases return no entry for that ordinal rather than guessing.
func resolveCUDAOrdinals(environ []byte, devices []gpuIdentity) map[uint32]int64 {
	// Parse the NUL-separated environment into a key→value map.
	env := make(map[string]string)
	for _, entry := range splitNUL(environ) {
		if idx := indexByte(entry, '='); idx >= 0 {
			env[entry[:idx]] = entry[idx+1:]
		}
	}

	visible, hasVisible := env["CUDA_VISIBLE_DEVICES"]
	order := env["CUDA_DEVICE_ORDER"]

	// An explicitly empty CUDA_VISIBLE_DEVICES hides all devices.
	if hasVisible && visible == "" {
		return map[uint32]int64{}
	}

	// Build a UUID→id lookup for exact matching.
	byUUID := make(map[string]int64, len(devices))
	for _, d := range devices {
		if d.device.UUID != "" {
			byUUID[d.device.UUID] = d.id
		}
	}

	// If CUDA_VISIBLE_DEVICES is not set every device is visible in its
	// natural (driver) order, which is safe to resolve when there is only one
	// device or when PCI_BUS_ID order is explicit.
	if !hasVisible {
		if len(devices) == 1 {
			return map[uint32]int64{0: devices[0].id}
		}
		if order == "PCI_BUS_ID" {
			out := make(map[uint32]int64, len(devices))
			for i, d := range devices {
				out[uint32(i)] = d.id
			}
			return out
		}
		// Ambiguous: CUDA picks devices by its own internal order which we
		// can't determine from here.
		return map[uint32]int64{}
	}

	// CUDA_VISIBLE_DEVICES is a comma-separated list of UUID or integer tokens.
	tokens := splitComma(visible)
	out := make(map[uint32]int64, len(tokens))
	for ordinal, token := range tokens {
		if token == "" {
			continue
		}
		// UUID token: GPU-xxxxxxxx-… prefix.
		if len(token) > 4 && token[:4] == "GPU-" {
			if id, ok := byUUID[token]; ok {
				out[uint32(ordinal)] = id
			}
			// Unknown UUID: ordinal not mapped (device not in our list).
			continue
		}
		// Integer index token.
		idx, err := strconv.Atoi(token)
		if err != nil || idx < 0 || idx >= len(devices) {
			continue
		}
		if order == "PCI_BUS_ID" || len(devices) == 1 {
			out[uint32(ordinal)] = devices[idx].id
		}
		// With default (FASTEST_FIRST) ordering and multiple devices the
		// mapping is ambiguous; leave the ordinal unmapped.
	}
	return out
}

// splitNUL splits a NUL-separated byte slice, skipping empty segments.
func splitNUL(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			if i > start {
				out = append(out, string(b[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

// splitComma splits a comma-separated string.
func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// indexByte returns the index of the first occurrence of c in s, or -1.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// processID validates cached identities against /proc so a missed exit event
// cannot carry an identity across PID reuse.
func (i *tracerIdentity) processID(ctx context.Context, database *db.DB, tgid uint32) (int64, error) {
	if tgid == 0 {
		return 0, fmt.Errorf("tracer: kernel reported tgid 0")
	}
	startTicks, command, err := core.ReadProcessIdentity(uint(tgid))
	if err != nil {
		return 0, err
	}
	i.mu.Lock()
	if cached, ok := i.tgids[tgid]; ok && cached.startTicks == startTicks {
		i.mu.Unlock()
		return cached.procID, nil
	}
	delete(i.ordinalsByTGID, tgid)
	i.mu.Unlock()

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
	i.tgids[tgid] = processCache{startTicks: startTicks, procID: procID}
	i.mu.Unlock()
	return procID, nil
}

// startTicksMatchBoottime checks whether the /proc start_ticks for a process
// match the start_boottime_ns the kernel stamped on an event.
//
// The kernel stores task_struct.start_boottime in nanoseconds. /proc reports
// start time in USER_HZ (100 Hz) ticks. Dividing by 10_000_000 converts ns
// to ticks (floor). Zero on either side means the value is unknown and can't
// match anything.
func startTicksMatchBoottime(startTicks int64, startBoottimeNs uint64) bool {
	if startBoottimeNs == 0 || startTicks <= 0 {
		return false
	}
	return startTicks == int64(startBoottimeNs/10_000_000)
}

func (i *tracerIdentity) cachedProcessID(tgid uint32, startBoottimeNs uint64) (int64, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	cached, ok := i.tgids[tgid]
	if !ok || !startTicksMatchBoottime(cached.startTicks, startBoottimeNs) {
		return 0, false
	}
	return cached.procID, true
}

// forget drops a cache entry only for the process generation named by the exit event.
func (i *tracerIdentity) forget(tgid uint32, startBoottimeNs uint64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if cached, ok := i.tgids[tgid]; ok && startTicksMatchBoottime(cached.startTicks, startBoottimeNs) {
		delete(i.tgids, tgid)
		delete(i.ordinalsByTGID, tgid)
	}
}
