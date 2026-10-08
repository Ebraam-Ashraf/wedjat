package ebpf

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// TracerConfig holds eBPF-specific configuration. It is intentionally separate
// from the top-level daemon Config so the source layer has no upward dependency
// on core.
type TracerConfig struct {
	// Enabled turns the tracer off entirely. NVML polling is unaffected.
	Enabled bool
	// RawCapture sends every event to userspace instead of only counting the
	// hot paths.
	RawCapture bool
	// SyncStallUs is how long one sync may take before it is recorded as a
	// stall. Zero means the default.
	SyncStallUs uint32
	// DrainTickMs is the interval (ms) at which eBPF aggregate maps are drained
	// into the database. Zero means the default (1000ms).
	DrainTickMs int
	// ObjectsDir holds the compiled BPF objects. Empty means the default.
	ObjectsDir string
	// PinDir holds the pinned state maps. Empty means the default.
	PinDir string
	// LibcudaPath overrides CUDA library discovery.
	LibcudaPath string
	// LibcudartPath overrides CUDA runtime library discovery.
	LibcudartPath string
	// FixLibcudaPermissions sets the execute bit on the CUDA driver library
	// when it is missing. eBPF uprobes are matched by inode, so tracing the
	// CUDA API requires the real library to be readable as executable.
	FixLibcudaPermissions bool
	// FixLibcudartPermissions sets the execute bit on the CUDA runtime library
	// when it is missing.
	FixLibcudartPermissions bool
}

// DefaultTracerConfig returns the recommended defaults for production.
func DefaultTracerConfig() TracerConfig {
	return TracerConfig{
		Enabled:                true,
		FixLibcudaPermissions:  true,
		FixLibcudartPermissions: true,
		DrainTickMs:            1000,
	}
}

// Session represents a running eBPF tracer session.
type Session struct {
	tracer      *Tracer
	drainTickMs int
	db          source.Chans
	sock        source.Chans

	cancel       context.CancelFunc
	wg           sync.WaitGroup
	once         sync.Once
	lastStats    Stats
	lastStatsLog time.Time
}

// Start loads the BPF objects, attaches what it can, and begins draining
// counters and events to the provided channels.
//
// Best-effort: disabled → (nil, nil); missing objects → (nil, nil) with log;
// load+attach failure → error.
func Start(ctx context.Context, cfg TracerConfig, db, sock source.Chans) (*Session, error) {
	if !cfg.Enabled {
		log.Println("eBPF: disabled by configuration")
		return nil, nil
	}

	libcuda := cfg.LibcudaPath
	if libcuda == "" {
		found, err := findLibcuda()
		if err != nil {
			if !ObjectsExist(cfg.ObjectsDir) {
				log.Printf("eBPF: objects not found and libcuda missing: %v", err)
				return nil, nil
			}
			// Objects exist but no libcuda — still try to load (kprobes may work)
			log.Printf("eBPF: libcuda not found: %v", err)
		} else {
			libcuda = found
		}
	}

	libcudart := cfg.LibcudartPath
	if libcudart == "" {
		found, err := findLibcudart()
		if err != nil {
			log.Printf("eBPF: libcudart not found: %v", err)
		} else {
			libcudart = found
		}
	}

	objectsDir, err := resolveObjectsDir(cfg.ObjectsDir)
	if err != nil {
		// Missing objects is best-effort: log and return nil
		log.Printf("eBPF: %v", err)
		return nil, nil
	}

	pinDir := cfg.PinDir
	if pinDir == "" {
		pinDir = defaultPinDir
	}

	if cfg.FixLibcudaPermissions && libcuda != "" {
		if err := ensureLibcudaExecutable(libcuda); err != nil {
			log.Printf("eBPF: could not make %s executable: %v", libcuda, err)
		}
	}

	if cfg.FixLibcudartPermissions && libcudart != "" {
		if err := ensureLibcudaExecutable(libcudart); err != nil {
			log.Printf("eBPF: could not make %s executable: %v", libcudart, err)
		}
	}

	tracer, err := LoadTracer(objectsDir, pinDir, libcuda, libcudart)
	if err != nil {
		return nil, err
	}

	if err := tracer.SetConfig(cfg.RawCapture, cfg.SyncStallUs); err != nil {
		log.Printf("eBPF: could not apply configuration: %v", err)
	}

	logAttachments(tracer, libcuda)

	sessionCtx, cancel := context.WithCancel(ctx)
	session := &Session{
		tracer:      tracer,
		cancel:      cancel,
		drainTickMs: cfg.DrainTickMs,
		db:          db,
		sock:        sock,
	}

	session.wg.Add(2)
	go func() {
		defer session.wg.Done()
		session.drainLoop(sessionCtx)
	}()
	go func() {
		defer session.wg.Done()
		session.consumeEvents(sessionCtx)
	}()

	return session, nil
}

// Close stops the tracer session and waits for goroutines to exit.
func (s *Session) Close() error {
	s.once.Do(func() {
		s.cancel()
		if s.tracer != nil && s.tracer.reader != nil {
			if err := s.tracer.reader.Close(); err != nil {
				log.Printf("eBPF: close event reader: %v", err)
			}
		}
		s.wg.Wait()
		if s.tracer != nil {
			s.tracer.Close()
		}
	})
	return nil
}

// Stats returns the current eBPF statistics.
func (s *Session) Stats() (Stats, error) {
	if s.tracer == nil {
		return Stats{}, nil
	}
	return s.tracer.Stats()
}

// drainLoop periodically drains the aggregate map and checks for hung syncs.
func (s *Session) drainLoop(ctx context.Context) {
	tick := s.drainTickMs
	if tick <= 0 {
		tick = 1000
	}

	ticker := time.NewTicker(time.Duration(tick) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			aggRows := s.drainAggregates()
			if len(aggRows) > 0 {
				source.Send(s.db.Agg, aggRows)
				if source.Clients.Load() > 0 {
					source.Send(s.sock.Agg, aggRows)
				}
			}

			s.scanHungSyncs(ctx)
			s.logCounterGrowth()
		}
	}
}

func (s *Session) logCounterGrowth() {
	stats, err := s.Stats()
	if err != nil {
		log.Printf("eBPF: read stats: %v", err)
		return
	}
	grew := stats.RingbufDrops > s.lastStats.RingbufDrops || stats.MapUpdateFailures > s.lastStats.MapUpdateFailures
	if grew && (s.lastStatsLog.IsZero() || time.Since(s.lastStatsLog) >= 10*time.Second) {
		log.Printf("Warning: eBPF drops increased: ringbuf=%d map_updates=%d", stats.RingbufDrops, stats.MapUpdateFailures)
		s.lastStatsLog = time.Now()
		s.lastStats = stats
	}
}

// ensureLibcudaExecutable sets the execute bit on the CUDA driver library.
func ensureLibcudaExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	const permExec = 0o111
	if info.Mode()&permExec != 0 {
		return nil
	}
	mode := info.Mode().Perm() | permExec
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	log.Printf("eBPF: NOTICE: set the execute bit on %s (%#o -> %#o). eBPF uprobes "+
		"require it and no CUDA probe can attach without it.",
		path, info.Mode().Perm(), mode)
	return nil
}

// logAttachments reports what is actually being traced.
func logAttachments(t *Tracer, libcuda string) {
	log.Printf("eBPF: attached %d probe(s), failed %d",
		len(t.Attached), len(t.Failed))
	if len(t.Failed) > 0 {
		log.Printf("eBPF: failed attachments: %v", t.Failed)
	}
}

// ObjectsExist reports whether a complete set of compiled BPF objects can be found.
func ObjectsExist(configured string) bool {
	_, err := resolveObjectsDir(configured)
	return err == nil
}
