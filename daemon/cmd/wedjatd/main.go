package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/bootstrap"
	"github.com/Ebraam-Ashraf/wedjat/daemon/collector"
	"github.com/Ebraam-Ashraf/wedjat/daemon/socket"
	"github.com/Ebraam-Ashraf/wedjat/daemon/store"
)

type storeStage struct {
	opts store.OpenOptions
	s    **store.Store
}

func (s *storeStage) Name() string { return "store" }
func (s *storeStage) Start(ctx context.Context) (bootstrap.StopFunc, error) {
	st, err := store.Open(ctx, s.opts)
	if err != nil {
		return nil, err
	}
	*s.s = st
	return st.Close, nil
}

type collectorStage struct {
	s      **store.Store
	handle **collector.Handle
}

func (c *collectorStage) Name() string { return "collector" }
func (c *collectorStage) Start(ctx context.Context) (bootstrap.StopFunc, error) {
	st := *c.s
	if st == nil {
		return nil, fmt.Errorf("store is not initialized")
	}
	handle, err := collector.Start(ctx, st)
	if err != nil {
		return nil, err
	}
	*c.handle = handle
	return handle.Stop, nil
}

// socketStage serves the live in-progress minute. It is a required consumer:
// the accumulator it reads is destroyed at every flush, so nothing else can
// answer "what is the GPU doing right now".
type socketStage struct {
	handle **collector.Handle
	path   string
}

func (s *socketStage) Name() string { return "socket" }
func (s *socketStage) Start(ctx context.Context) (bootstrap.StopFunc, error) {
	handle := *s.handle
	if handle == nil {
		return nil, fmt.Errorf("collector is not initialized")
	}
	return socket.Start(ctx, socket.Options{
		Path:   s.path,
		Source: liveSource(handle),
		// Never faster than the collector's poll interval: a quicker tick
		// would resend an identical snapshot.
		BroadcastInterval: 2 * time.Second,
	})
}

// liveSource adapts the collector's in-memory sample to the socket wire format.
func liveSource(handle *collector.Handle) socket.Source {
	return func() socket.Snapshot {
		live := handle.Live()
		snapshot := socket.Snapshot{
			UnixNano:   live.UnixNano,
			MinuteUnix: live.MinuteUnix,
			GPUs:       make([]socket.GPUSnapshot, 0, len(live.GPUs)),
			Processes:  make([]socket.ProcessSnapshot, 0, len(live.Procs)),
		}
		for _, g := range live.GPUs {
			snapshot.GPUs = append(snapshot.GPUs, socket.GPUSnapshot{
				Index:       g.Index,
				UtilGPU:     g.UtilGPU,
				UtilMem:     g.UtilMem,
				TempC:       g.TempC,
				PowerMW:     g.PowerMW,
				VRAMUsed:    g.VRAMUsed,
				SMClockMHz:  g.SMClockMHz,
				MemClockMHz: g.MemClockMHz,
				Valid:       g.Valid,
			})
		}
		for _, p := range live.Procs {
			snapshot.Processes = append(snapshot.Processes, socket.ProcessSnapshot{
				PID:       p.PID,
				GPUIndex:  p.GPUIndex,
				VRAMBytes: p.VRAMBytes,
				VRAMValid: p.VRAMValid,
			})
		}
		return snapshot
	}
}

func main() {
	devMode := flag.Bool("dev", false, "Run in local development mode")
	configPath := flag.String("config", "", "Path to config file")
	flag.Parse()

	if *devMode {
		// Collision Guard
		cmd := exec.Command("systemctl", "is-active", "--quiet", "wedjatd.service")
		if err := cmd.Run(); err == nil {
			log.Fatalf("FATAL: wedjatd.service is actively running. Stop it before running in --dev mode to prevent eBPF collisions.")
		}
	}

	cfgPath := *configPath
	var dataDir, lockPath string

	if *devMode {
		if cfgPath == "" {
			cfgPath = "./dev/etc/wedjat/config.yaml"
		}
		dataDir = "./dev/var/lib/wedjat"
		lockPath = "/tmp/wedjat-dev.lock"
		log.Println("==> Running in DEV MODE (Isolated DBs and unpinned BPF paths)")
	} else {
		if cfgPath == "" {
			cfgPath = "/etc/wedjat/config.yaml"
		}
		dataDir = "/var/lib/wedjat"
		lockPath = "/run/wedjat/daemon.lock"
	}

	// We don't read config from file in this minimal example, but normally you'd parse cfgPath.
	// We'll just construct the Store options.
	if !filepath.IsAbs(lockPath) && *devMode {
		absLock, _ := filepath.Abs(lockPath)
		lockPath = absLock
	}
	if !filepath.IsAbs(dataDir) && *devMode {
		absData, _ := filepath.Abs(dataDir)
		dataDir = absData
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var st *store.Store
	var collectorHandle *collector.Handle

	storeOpts := store.OpenOptions{
		DataDir:      dataDir,
		LockPath:     lockPath,
		ResetOnBoot:  true,
		AllowHomeDir: *devMode,
	}

	stages := []bootstrap.Stage{
		&storeStage{opts: storeOpts, s: &st},
		&collectorStage{s: &st, handle: &collectorHandle},
		&socketStage{handle: &collectorHandle, path: socket.PathForMode(*devMode)},
	}

	runtime, err := bootstrap.Start(ctx, stages...)
	if err != nil {
		log.Fatalf("Failed to start daemon: %v", err)
	}

	log.Println("wedjatd is running. Press Ctrl+C to stop.")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	sig := <-sigCh
	log.Printf("Received signal %s, initiating clean shutdown...", sig)
	cancel() // Cancel context

	if err := runtime.Close(context.Background()); err != nil {
		log.Fatalf("Errors during shutdown: %v", err)
	}
	log.Println("Clean shutdown complete.")
}
