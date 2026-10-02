package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/ebpf"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/nvml"
)

var (
	// Development paths - relative to working directory
	configPath  = "./dev/etc/wedjat/config.yaml"
	dataDir     = "./dev/var/lib/wedjat"
	lockFile    = "./dev/run/wedjat.lock"
	socketPath  = "./dev/run/wedjat.sock"
	socketGroup = "" // No group in dev mode
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("FATAL: %v", err)
	}
}

func run() error {
	log.Println("==> Starting wedjatd-dev in DEVELOPMENT MODE")

	// Prevent collision with the installed system daemon
	cmd := exec.Command("systemctl", "is-active", "--quiet", "wedjatd.service")
	if err := cmd.Run(); err == nil {
		return fmt.Errorf("wedjatd.service is running. Stop it first to prevent eBPF conflicts:\n  sudo systemctl stop wedjatd")
	}

	// Resolve relative paths to absolute
	var err error
	paths := Paths{
		ConfigFile:  configPath,
		DataDir:     dataDir,
		LockFile:    lockFile,
		SocketPath:  socketPath,
		SocketGroup: socketGroup,
	}

	paths.ConfigFile, err = filepath.Abs(paths.ConfigFile)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	paths.DataDir, err = filepath.Abs(paths.DataDir)
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}
	paths.LockFile, err = filepath.Abs(paths.LockFile)
	if err != nil {
		return fmt.Errorf("resolve lock file: %w", err)
	}
	paths.SocketPath, err = filepath.Abs(paths.SocketPath)
	if err != nil {
		return fmt.Errorf("resolve socket path: %w", err)
	}

	log.Printf("    Config: %s", paths.ConfigFile)
	log.Printf("    DataDir: %s", paths.DataDir)
	log.Printf("    Socket: %s", paths.SocketPath)

	// 1. Acquire lock to prevent double-run
	lock, err := acquireLock(paths.LockFile)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Release()
	log.Printf("Acquired lock: %s", paths.LockFile)

	// 2. Configuration
	if err := core.EnsureConfig(paths.ConfigFile); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := core.LoadConfig(paths.ConfigFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log.Println("Configuration loaded")

	// 3. Open database
	ctx := context.Background()
	database, err := db.OpenDB(ctx, paths.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	log.Printf("Database opened: %s", paths.DataDir)

	// Boot ID. It is trimmed once here so every table stores the same value;
	// a trailing newline would silently break joins on boot_id.
	bootID, err := core.ReadBootID()
	if err != nil {
		return fmt.Errorf("read boot ID: %w", err)
	}

	// Report on the previous run before overwriting its state.
	previousClean, err := database.PreviousCleanShutdown(ctx)
	if err != nil {
		return fmt.Errorf("read previous shutdown state: %w", err)
	}
	if !previousClean {
		log.Println("Warning: the previous dev run did not shut down cleanly")
	}

	// A PID is only unique within one boot, so processes left open by an
	// earlier boot can never be matched again and are closed as rebooted.
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

	// Mark clean shutdown as false (will be set to true on exit)
	if err := database.WriteCleanShutdown(ctx, false); err != nil {
		return fmt.Errorf("write shutdown state: %w", err)
	}

	// 4. Hand the daily database lifecycle to the timer. The wait group makes
	// shutdown wait for it, otherwise the timer could still be inside a
	// rotation while the deferred database.Close runs.
	timerCtx, stopTimer := context.WithCancel(ctx)
	var timerWG sync.WaitGroup
	timerWG.Add(1)
	go func() {
		defer timerWG.Done()
		db.RunDayTimer(timerCtx, database, cfg.Retention.DayFilesDays)
	}()
	defer func() {
		stopTimer()
		timerWG.Wait()
	}()

	// 5. Initialize NVML
	if err := nvml.InitSources(); err != nil {
		return fmt.Errorf("init sources: %w", err)
	}
	defer nvml.ShutdownSources()
	log.Println("NVML initialized")

	// Discover GPUs
	devices, err := nvml.DiscoverDevices()
	if err != nil {
		return fmt.Errorf("discover devices: %w", err)
	}
	log.Printf("Discovered %d GPU(s)", len(devices))

	if err := core.RegisterDevices(ctx, database, devices); err != nil {
		return fmt.Errorf("register devices: %w", err)
	}

	gpuUUIDs := make([]string, len(devices))
	for i, device := range devices {
		gpuUUIDs[i] = device.UUID
		log.Printf("  GPU %d: %s (%s)", device.Index, device.UUID, device.Name)
	}

	// 6. Start socket server. Clients are served from the snapshot the poll
	// loop already produced, so a client costs nothing instead of triggering
	// its own NVML sweep.
	var latest atomic.Pointer[core.Snapshot]
	socketServer, err := core.StartSocketWithOptions(paths.SocketPath, func() core.Snapshot {
		if cached := latest.Load(); cached != nil {
			return *cached
		}
		return core.Snapshot{}
	}, core.SocketOptions{Group: paths.SocketGroup})
	if err != nil {
		return fmt.Errorf("start socket: %w", err)
	}
	defer socketServer.Stop()
	log.Printf("Socket server started: %s", paths.SocketPath)

	// 7. Start the eBPF tracer. It is best effort: without the privileges to
	// load BPF, or on a driver whose symbols moved, the daemon keeps running on
	// NVML alone and says so rather than refusing to start.
	tracerSession, err := ebpf.StartTracer(ctx, database, bootID, devices, cfg.Tracing)
	if err != nil {
		if !ebpf.ObjectsExist(cfg.Tracing.ObjectsDir) {
			log.Printf("Warning: eBPF objects not built under %q, continuing with NVML only "+
				"(run 'make bpf')", cfg.Tracing.ObjectsDir)
		} else {
			log.Printf("Warning: eBPF tracing unavailable: %v; continuing with NVML only", err)
		}
	} else if tracerSession != nil {
		defer tracerSession.Close()
	}

	// 8. Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	log.Println("wedjatd-dev is running. Press Ctrl+C to stop.")

	// 9. Main loop: poll and heartbeat
	pollTicker := time.NewTicker(2 * time.Second)
	defer pollTicker.Stop()

	heartbeatTicker := time.NewTicker(10 * time.Second)
	defer heartbeatTicker.Stop()

	// Kernel counters are cumulative, so this keeps the last value seen and
	// only a growth is worth logging.
	var lastStats ebpf.Stats

	for {
		select {
		case <-pollTicker.C:
			snapshot := core.BuildSnapshot(gpuUUIDs)
			latest.Store(&snapshot)
			if err := core.Record(ctx, database, bootID, snapshot); err != nil {
				log.Printf("Warning: failed to record samples: %v", err)
			}

		case <-heartbeatTicker.C:
			if err := database.WriteHeartbeat(ctx); err != nil {
				log.Printf("Warning: failed to write heartbeat: %v", err)
			}
			// Ring buffer drops mean the daemon could not keep up with the
			// event stream and the kernel discarded records. That is silent
			// data loss unless it is reported. The counters are cumulative, so
			// only the growth since the last check is reported.
			if tracerSession != nil {
				stats, err := tracerSession.Stats()
				if err == nil {
					if grew := stats.RingbufDrops > lastStats.RingbufDrops ||
						stats.MapUpdateFailures > lastStats.MapUpdateFailures ||
						stats.AllocFreeMisses > lastStats.AllocFreeMisses ||
						stats.Unattributed > lastStats.Unattributed; grew {
						log.Printf("Warning: eBPF events not fully recorded: ringbuf_drops=+%d "+
							"map_update_failures=+%d alloc_free_misses=+%d unattributed=+%d unknown_device=%d",
							stats.RingbufDrops-lastStats.RingbufDrops,
							stats.MapUpdateFailures-lastStats.MapUpdateFailures,
							stats.AllocFreeMisses-lastStats.AllocFreeMisses,
							stats.Unattributed-lastStats.Unattributed,
							stats.UnknownDeviceEvents)
					}
					lastStats = stats
				}
			}

		case sig := <-sigCh:
			log.Printf("Received signal %s, initiating clean shutdown...", sig)

			// Mark clean shutdown
			if err := database.WriteCleanShutdown(ctx, true); err != nil {
				log.Printf("Warning: failed to mark clean shutdown: %v", err)
			}

			log.Println("Clean shutdown complete.")
			return nil
		}
	}
}

// Paths holds the filesystem locations the daemon reads from and writes to.
type Paths struct {
	ConfigFile  string
	DataDir     string
	LockFile    string
	SocketPath  string
	SocketGroup string
}

// Lock represents an exclusive file lock that prevents multiple daemon instances.
type Lock struct {
	file *os.File
	mu   sync.Mutex
	open bool
}

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
		return nil, fmt.Errorf("%s (hint: this usually means the file was created by a different user; remove it, or always run the daemon with the same privileges)", describeLockError(path, err))
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

func describeLockError(path string, err error) error {
	if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("open lock file %s: %w", path, err)
	}
	owner := "another user"
	if info, statErr := os.Stat(path); statErr == nil {
		if uid, uidOK := info.Sys().(*syscall.Stat_t); uidOK {
			owner = fmt.Sprintf("uid %d", uid.Uid)
		}
	}
	return fmt.Errorf("open lock file %s: %w (owned by %s)", path, err, owner)
}

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
