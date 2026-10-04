// Package daemon contains the shared startup, poll loop, and shutdown logic
// used by all wedjat daemon binaries. It sits above core and core/source in
// the import graph and is kept as an internal package so it is not
// accidentally imported by packages that would close a cycle.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
	coredb "github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
	sourceebpf "github.com/Ebraam-Ashraf/wedjat/daemon/core/source/ebpf"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source/nvml"
)

// Paths holds the filesystem locations the daemon reads from and writes to.
type Paths struct {
	ConfigFile  string
	DataDir     string
	LockFile    string
	SocketPath  string
	SocketGroup string
}

// Run runs the full daemon lifecycle: lock, config, database, NVML, eBPF,
// socket, and clean shutdown. It blocks until a signal is received.
//
// preRun, when non-nil, is called before the lock is acquired. It is the
// caller's hook for mode-specific checks (e.g. the dev-mode systemd guard).
// Returning an error from preRun aborts startup.
func Run(paths Paths, readyMsg string, preRun func() error) error {
	if preRun != nil {
		if err := preRun(); err != nil {
			return err
		}
	}

	// 1. Acquire lock to prevent double-run.
	lock, err := acquireLock(paths.LockFile)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Release()
	log.Printf("Acquired lock: %s", paths.LockFile)

	// 2. Configuration.
	if err := core.EnsureConfig(paths.ConfigFile); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := core.LoadConfig(paths.ConfigFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	log.Println("Configuration loaded")

	// 3. Open database.
	ctx := context.Background()
	database, err := coredb.OpenDB(ctx, paths.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	log.Printf("Database opened: %s", paths.DataDir)
	if _, _, err := database.PruneMeta(ctx, time.Now().UTC(), cfg.Storage.ProcessesDays, cfg.Storage.IncidentsDays); err != nil {
		return fmt.Errorf("prune metadata: %w", err)
	}

	// 4. Boot ID.
	bootID, err := ReadBootID()
	if err != nil {
		return fmt.Errorf("read boot ID: %w", err)
	}

	// 5. Previous run checks.
	previousClean, err := database.PreviousCleanShutdown(ctx)
	if err != nil {
		return fmt.Errorf("read previous shutdown state: %w", err)
	}
	if !previousClean {
		log.Println("Warning: the previous daemon run did not shut down cleanly")
	}

	closed, err := database.CloseProcessesFromOtherBoots(ctx, bootID, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("close processes from earlier boots: %w", err)
	}
	if closed > 0 {
		log.Printf("Closed %d process(es) left running by a previous boot", closed)
	}

	if err := database.WriteBootID(ctx, bootID); err != nil {
		return fmt.Errorf("write boot ID: %w", err)
	}
	if err := database.WriteCleanShutdown(ctx, false); err != nil {
		return fmt.Errorf("write shutdown state: %w", err)
	}

	// 6. Daily database lifecycle timer.
	timerCtx, stopTimer := context.WithCancel(ctx)
	var timerWG sync.WaitGroup
	timerWG.Add(1)
	go func() {
		defer timerWG.Done()
		coredb.RunDayTimerWithMeta(timerCtx, database, cfg.Storage.DayFilesDays,
			cfg.Storage.ProcessesDays, cfg.Storage.IncidentsDays)
	}()
	defer func() {
		stopTimer()
		timerWG.Wait()
	}()

	// 7. Initialize NVML. Best effort: a host without the NVIDIA driver still
	// benefits from eBPF-based telemetry.
	var devices []source.DeviceInfo
	var gpuUUIDs []string
	nvmlAvailable := false
	nvmlShutdown := false

	if err := nvml.InitSources(); err != nil {
		log.Printf("Warning: NVML unavailable: %v; continuing without NVML telemetry", err)
	} else {
		nvmlAvailable = true
		defer func() {
			if nvmlAvailable && !nvmlShutdown {
				nvml.ShutdownSources()
			}
		}()
		log.Println("NVML initialized")

		discovered, err := nvml.DiscoverDevices()
		if err != nil {
			log.Printf("Warning: GPU discovery failed: %v; continuing without NVML telemetry", err)
		} else {
			devices = discovered
			log.Printf("Discovered %d GPU(s)", len(devices))
		}
	}

	// 8. Register devices and build channels.
	if _, err := database.RegisterDevices(ctx, devices); err != nil {
		return fmt.Errorf("register devices: %w", err)
	}

	gpuUUIDs = make([]string, len(devices))
	for i, d := range devices {
		gpuUUIDs[i] = d.UUID
		log.Printf("  GPU %d: %s (%s)", d.Index, d.UUID, d.Name)
	}

	dbc := source.NewChans(4096)
	sc := source.NewChans(64)

	// 9. Start DB layer.
	layer := coredb.StartLayer(ctx, database, bootID, devices, dbc)
	defer layer.Stop()
	log.Println("DB layer started")

	// 10. Start socket server.
	socketServer, err := core.StartSocketWithOptions(
		paths.SocketPath,
		sc,
		core.SocketOptions{Group: paths.SocketGroup},
	)
	if err != nil {
		return fmt.Errorf("start socket: %w", err)
	}
	defer socketServer.Stop()
	log.Printf("Socket server started: %s", paths.SocketPath)

	// 11. Start NVML polling goroutine (best effort — only when NVML init succeeded).
	var cancelNVML context.CancelFunc
	var nvmlDone chan struct{}
	if nvmlAvailable && len(gpuUUIDs) > 0 {
		nvmlCtx, cancel := context.WithCancel(ctx)
		cancelNVML = cancel
		nvmlDone = make(chan struct{})
		go func() {
			defer close(nvmlDone)
			nvml.Run(nvmlCtx, dbc, sc, gpuUUIDs, cfg.Polling.NvmlDbTickMs, cfg.Polling.NvmlSocketTickMs)
		}()
		log.Println("NVML polling started")
	}

	// 12. Start eBPF tracer. Best effort.
	tracerCfg := sourceebpf.DefaultTracerConfig()
	tracerCfg.DrainTickMs = cfg.Polling.EbpfDrainTickMs
	tracerSession, err := sourceebpf.Start(ctx, tracerCfg, dbc, sc)
	if err != nil {
		if !sourceebpf.ObjectsExist(tracerCfg.ObjectsDir) {
			log.Printf("Warning: eBPF objects not built, continuing with NVML only " +
				"(run 'make bpf')")
		} else {
			log.Printf("Warning: eBPF tracing unavailable: %v; continuing with NVML only", err)
		}
	} else if tracerSession != nil {
		log.Println("eBPF tracer started")
	}

	// 13. Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	log.Println(readyMsg)

	// 14. Wait for shutdown signal.
	sig := <-sigCh
	log.Printf("Received signal %s, initiating clean shutdown...", sig)

	// Shutdown order: eBPF → NVML → socket → layer → clean shutdown flag.
	if tracerSession != nil {
		if err := tracerSession.Close(); err != nil {
			log.Printf("Warning: eBPF session close: %v", err)
		}
	}
	if cancelNVML != nil {
		cancelNVML()
		<-nvmlDone
	}
	if nvmlAvailable {
		nvml.ShutdownSources()
		nvmlShutdown = true
	}
	if err := socketServer.Stop(); err != nil {
		log.Printf("Warning: socket shutdown: %v", err)
	}
	if err := layer.Stop(); err != nil {
		log.Printf("Warning: DB layer final flush failed; shutdown will remain unclean: %v", err)
		return err
	}
	stopTimer()
	timerWG.Wait()

	if err := database.WriteCleanShutdown(ctx, true); err != nil {
		log.Printf("Warning: failed to mark clean shutdown: %v", err)
		return err
	}
	if err := database.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	log.Println("Clean shutdown complete.")
	return nil
}

// Lock represents an exclusive file lock that prevents multiple daemon instances.
type Lock struct {
	file *os.File
	mu   sync.Mutex
	open bool
}

// acquireLock opens and exclusively flocks path. The path must be a clean
// absolute path so it cannot be confused with a relative one left over from a
// different working directory.
func acquireLock(path string) (*Lock, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("lock path must be a clean absolute path")
	}
	lockDir := filepath.Dir(path)
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("%w", describeLockError(path, err))
	}
	file := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("another daemon instance is already running (lock held: %s)", path)
		}
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return &Lock{file: file, open: true}, nil
}

// describeLockError enriches a permission error with the owning uid.
func describeLockError(path string, err error) error {
	if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("open lock file %s: %w", path, err)
	}
	owner := "another user"
	if info, statErr := os.Stat(path); statErr == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			owner = fmt.Sprintf("uid %d", st.Uid)
		}
	}
	return fmt.Errorf("open lock file %s: %w (owned by %s)", path, err, owner)
}

// Release unlocks and closes the lock file. Safe to call on nil or multiple times.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.open {
		return nil
	}
	l.open = false
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	return errors.Join(unlockErr, closeErr)
}
