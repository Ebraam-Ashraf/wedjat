// Package nvml implements the NVML source that polls GPU hardware and processes.
package nvml

import (
	"context"
	"log"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// slowNVMLCall is deliberately below the live sampling interval. A call that
// takes this long is capable of making the 500ms telemetry cadence miss ticks.
const slowNVMLCall = 250 * time.Millisecond

// xidPollInterval is intentionally short but non-blocking. The NVML event
// wait shares the poller's safety mutex with normal GPU reads. Giving that
// wait a one-second timeout monopolizes the mutex and can starve live GPU
// telemetry. Events remain queued by NVML until this loop reads them.
const xidPollInterval = 100 * time.Millisecond

// Run polls GPU hardware and processes, sending to the provided channels.
// GPU sampling adapts to client count. Process enumeration runs separately at
// the slower database cadence, avoiding process queries on every live sample
// and reducing contention on NVML's serialized API calls.
func Run(ctx context.Context, db, sock source.Chans, gpuUUIDs []string, dbTickMs, socketTickMs int) {
	if len(gpuUUIDs) == 0 {
		return
	}
	dbTick := time.Duration(dbTickMs) * time.Millisecond
	socketTick := time.Duration(socketTickMs) * time.Millisecond
	if dbTick <= 0 {
		dbTick = 2 * time.Second
	}
	if socketTick <= 0 {
		socketTick = 500 * time.Millisecond
	}

	xidDone := make(chan struct{})
	go func() { defer close(xidDone); runXid(ctx, db, sock) }()
	defer func() { <-xidDone }()

	processDone := make(chan struct{})
	go func() {
		defer close(processDone)
		runProcessPoller(ctx, db, sock, gpuUUIDs, dbTick)
	}()
	defer func() { <-processDone }()

	// Start with DB interval; switch to faster when clients connect
	interval := dbTick
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastDBSend, lastSocketSend time.Time
	var lastGPUCycle time.Time
	gpuPollFailed := make(map[string]bool, len(gpuUUIDs))

	for {
		select {
		case <-ctx.Done():
			return

		case tickAt := <-ticker.C:
			cycleStarted := time.Now()
			if !lastGPUCycle.IsZero() {
				gap := cycleStarted.Sub(lastGPUCycle)
				if gap > 2*interval {
					log.Printf("NVML GPU cycle gap: %s (target=%s clients=%d)", gap.Round(time.Millisecond), interval, source.Clients.Load())
				}
			}
			lastGPUCycle = cycleStarted
			if lag := cycleStarted.Sub(tickAt); lag > slowNVMLCall {
				log.Printf("NVML GPU ticker delayed by %s (target=%s)", lag.Round(time.Millisecond), interval)
			}

			// Check if we need to adjust polling interval based on client count
			clientsConnected := source.Clients.Load() > 0
			newInterval := dbTick
			if clientsConnected && socketTick < newInterval {
				newInterval = socketTick
			}

			// Reset ticker if interval changed
			if newInterval != interval {
				ticker.Reset(newInterval)
				interval = newInterval
			}

			// Keep process snapshot calls out of this loop. NVML serializes its
			// own API calls, so a process query already in progress can still
			// contend briefly with these polls. Report failures on transitions.
			gpus := make([]source.GPUSample, 0, len(gpuUUIDs))
			for index, uuid := range gpuUUIDs {
				if uuid == "" {
					continue
				}
				queryStarted := time.Now()
				gpu, err := PollGPU(uuid)
				if elapsed := time.Since(queryStarted); elapsed > slowNVMLCall {
					log.Printf("NVML GPU query slow for %s: %s", uuid, elapsed.Round(time.Millisecond))
				}
				if err != nil {
					if !gpuPollFailed[uuid] {
						log.Printf("NVML GPU poll failed for %s: %v", uuid, err)
					}
					gpuPollFailed[uuid] = true
					continue
				}
				if gpuPollFailed[uuid] {
					log.Printf("NVML GPU poll recovered for %s", uuid)
					delete(gpuPollFailed, uuid)
				}
				gpu.Index = uint(index)
				gpus = append(gpus, gpu)
			}
			now := time.Now()
			if elapsed := now.Sub(cycleStarted); elapsed > slowNVMLCall {
				log.Printf("NVML GPU cycle took %s for %d/%d device(s)", elapsed.Round(time.Millisecond), len(gpus), len(gpuUUIDs))
			}

			// Send to DB channel if enough time has passed
			if len(gpus) > 0 && (lastDBSend.IsZero() || now.Sub(lastDBSend) >= dbTick) {
				source.SendGPU(db.GPU, gpus)
				lastDBSend = now
			}

			// Send to socket channels if clients are connected and enough time has passed
			if clientsConnected && len(gpus) > 0 && (lastSocketSend.IsZero() || now.Sub(lastSocketSend) >= socketTick) {
				source.SendGPU(sock.GPU, gpus)
				lastSocketSend = now
			}
		}
	}
}

func runProcessPoller(ctx context.Context, db, sock source.Chans, gpuUUIDs []string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failed := make(map[string]bool, len(gpuUUIDs))
	var lastCycle time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case tickAt := <-ticker.C:
			cycleStarted := time.Now()
			if !lastCycle.IsZero() {
				gap := cycleStarted.Sub(lastCycle)
				if gap > 2*interval {
					log.Printf("NVML process cycle gap: %s (target=%s)", gap.Round(time.Millisecond), interval)
				}
			}
			lastCycle = cycleStarted
			if lag := cycleStarted.Sub(tickAt); lag > slowNVMLCall {
				log.Printf("NVML process ticker delayed by %s", lag.Round(time.Millisecond))
			}

			allProcs := make([]source.ProcessSample, 0)
			complete := true
			for _, uuid := range gpuUUIDs {
				if uuid == "" {
					complete = false
					continue
				}
				queryStarted := time.Now()
				rows, err := PollProcesses(uuid)
				if elapsed := time.Since(queryStarted); elapsed > slowNVMLCall {
					log.Printf("NVML process query slow for %s: %s", uuid, elapsed.Round(time.Millisecond))
				}
				allProcs = append(allProcs, rows...)
				if err != nil {
					complete = false
					if !failed[uuid] {
						log.Printf("NVML process poll failed for %s: %v", uuid, err)
					}
					failed[uuid] = true
				} else if failed[uuid] {
					log.Printf("NVML process poll recovered for %s", uuid)
					delete(failed, uuid)
				}
			}

			procList := source.ProcList{
				TsNano:   time.Now().UnixNano(),
				Procs:    allProcs,
				Complete: complete,
			}
			if elapsed := time.Since(cycleStarted); elapsed > slowNVMLCall {
				log.Printf("NVML process cycle took %s (%d process row(s), complete=%t)", elapsed.Round(time.Millisecond), len(allProcs), complete)
			}
			source.Send(db.Procs, procList)
			if source.Clients.Load() > 0 {
				source.Send(sock.Procs, procList)
			}
		}
	}
}

func pollAll(gpuUUIDs []string, pollGPU func(string) (source.GPUSample, error), pollProcs func(string) ([]source.ProcessSample, error)) ([]source.GPUSample, []source.ProcessSample, bool) {
	gpus := make([]source.GPUSample, 0, len(gpuUUIDs))
	procs := make([]source.ProcessSample, 0)
	complete := true
	for index, uuid := range gpuUUIDs {
		if uuid == "" {
			complete = false
			continue
		}
		gpu, err := pollGPU(uuid)
		if err != nil {
			complete = false
			continue
		}
		gpu.Index = uint(index)
		gpus = append(gpus, gpu)
		rows, err := pollProcs(uuid)
		procs = append(procs, rows...)
		if err != nil {
			complete = false
		}
	}
	return gpus, procs, complete
}

func runXid(ctx context.Context, db, sock source.Chans) {
	set := createXidSet()
	if set == nil {
		log.Printf("NVML Xid: could not create event set")
		return
	}
	defer destroyXidSet(set)
	ticker := time.NewTicker(xidPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// timeout=0 is a non-blocking NVML event-set poll. Holding the
		// poller mutex for the old one-second timeout stopped PollGPU from
		// obtaining its own NVML snapshot for whole seconds at a time.
		xid, result := waitXid(set, 0)
		switch result {
		case 0:
			source.Send(db.Xid, xid)
			if source.Clients.Load() > 0 {
				source.Send(sock.Xid, xid)
			}
		case 1:
			continue
		case 3:
			log.Printf("NVML Xid: event monitoring is unsupported by this driver")
			return
		default:
			log.Printf("NVML Xid: event wait failed; retrying")
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
