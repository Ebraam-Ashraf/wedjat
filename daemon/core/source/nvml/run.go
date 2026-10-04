// Package nvml implements the NVML source that polls GPU hardware and processes.
package nvml

import (
	"context"
	"log"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// Run polls GPU hardware and processes, sending to the provided channels.
// It respects client count for adaptive timing and sends to socket channels
// only when clients are connected.
func Run(ctx context.Context, db, sock source.Chans, gpuUUIDs []string, dbTickMs, socketTickMs int) {
	if len(gpuUUIDs) == 0 {
		return
	}
	xidDone := make(chan struct{})
	go func() { defer close(xidDone); runXid(ctx, db, sock) }()
	defer func() { <-xidDone }()

	dbTick := time.Duration(dbTickMs) * time.Millisecond
	socketTick := time.Duration(socketTickMs) * time.Millisecond

	// Start with DB interval; switch to faster when clients connect
	interval := dbTick
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastDBSend, lastSocketSend time.Time

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
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

			// Poll all GPUs and their processes
			now := time.Now()
			gpus, allProcs, processesComplete := pollAll(gpuUUIDs, PollGPU, PollProcesses)

			procList := source.ProcList{
				TsNano:   now.UnixNano(),
				Procs:    allProcs,
				Complete: processesComplete,
			}

			// Send to DB channel if enough time has passed
			if lastDBSend.IsZero() || now.Sub(lastDBSend) >= dbTick {
				source.Send(db.GPU, gpus)
				source.Send(db.Procs, procList)
				lastDBSend = now
			}

			// Send to socket channels if clients are connected and enough time has passed
			if clientsConnected && (lastSocketSend.IsZero() || now.Sub(lastSocketSend) >= socketTick) {
				source.Send(sock.GPU, gpus)
				source.Send(sock.Procs, procList)
				lastSocketSend = now
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
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		xid, result := waitXid(set, 1000)
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
