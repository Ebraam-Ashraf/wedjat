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

	"github.com/Ebraam-Ashraf/wedjat/daemon/bootstrap"
	"github.com/Ebraam-Ashraf/wedjat/daemon/collector"
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
	s **store.Store
}

func (c *collectorStage) Name() string { return "collector" }
func (c *collectorStage) Start(ctx context.Context) (bootstrap.StopFunc, error) {
	st := *c.s
	if st == nil {
		return nil, fmt.Errorf("store is not initialized")
	}
	stop, err := collector.Start(ctx, st)
	if err != nil {
		return nil, err
	}
	return stop, nil
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
			cfgPath = "./dev-config.yaml"
		}
		dataDir = "./dev-data"
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

	storeOpts := store.OpenOptions{
		DataDir:     dataDir,
		LockPath:    lockPath,
		ResetOnBoot: true,
	}

	stages := []bootstrap.Stage{
		&storeStage{opts: storeOpts, s: &st},
		&collectorStage{s: &st},
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
