package ebpf

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/db"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/nvml"
)

// TracerSession is a loaded tracer running against an open database. It owns the
// drain loop and the event consumer, and both stop when its context is
// cancelled.
type TracerSession struct {
	tracer *Tracer
	ids    *tracerIdentity

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// StartTracer loads the BPF objects, attaches what it can, and begins draining
// counters into the database.
//
// A failure here is not fatal. Loading eBPF needs privileges the daemon does not
// always have, and a missing driver symbol only costs the probes that depend on
// it, so the caller gets (nil, nil) and keeps running on NVML alone rather than
// losing GPU telemetry entirely.
func StartTracer(ctx context.Context, database *db.DB, bootID string, devices []nvml.DeviceInfo, cfg core.TracingConfig) (*TracerSession, error) {
	if !cfg.Enabled {
		log.Println("Tracer: disabled by configuration, continuing with NVML only")
		return nil, nil
	}

	libcuda := cfg.LibcudaPath
	if libcuda == "" {
		found, err := findLibcuda()
		if err != nil {
			return nil, err
		}
		libcuda = found
	}

	objectsDir, err := resolveObjectsDir(cfg.ObjectsDir)
	if err != nil {
		return nil, err
	}
	pinDir := cfg.PinDir
	if pinDir == "" {
		pinDir = defaultPinDir
	}

	if cfg.FixLibcudaPermissions {
		if err := ensureLibcudaExecutable(libcuda); err != nil {
			// Not fatal: the load below will fail loudly and the grouped
			// report names the fix.
			log.Printf("Tracer: could not make %s executable: %v", libcuda, err)
		}
	}

	tracer, err := LoadTracer(objectsDir, pinDir, libcuda)
	if err != nil {
		return nil, fmt.Errorf("load tracer from %s: %w", objectsDir, err)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	session := &TracerSession{tracer: tracer, ids: newTracerIdentity(bootID), cancel: cancel}

	if err := session.bindDevices(sessionCtx, database, devices); err != nil {
		cancel()
		tracer.Close()
		return nil, err
	}

	if err := tracer.SetConfig(cfg.RawCapture, cfg.SyncStallUs); err != nil {
		log.Printf("Tracer: could not apply configuration: %v", err)
	}

	session.logAttachments(libcuda)

	session.wg.Add(2)
	go func() {
		defer session.wg.Done()
		session.drainLoop(sessionCtx, database)
	}()
	go func() {
		defer session.wg.Done()
		tracer.consumeEvents(sessionCtx, database, session.ids)
	}()

	return session, nil
}

// bindDevices maps each kernel device ordinal onto the database row for that
// GPU. Without this every aggregate would be unattributable, so it is done once
// at startup rather than per event.
func (s *TracerSession) bindDevices(ctx context.Context, database *db.DB, devices []nvml.DeviceInfo) error {
	for _, device := range devices {
		if device.UUID == "" {
			continue
		}
		gpuID, err := database.GPUIDByUUID(ctx, device.UUID)
		if err != nil {
			return fmt.Errorf("resolve GPU %s: %w", device.UUID, err)
		}
		s.ids.setOrdinal(uint32(device.Index), gpuID)
	}
	return nil
}

// ensureLibcudaExecutable makes the CUDA driver library readable as executable
// when the operator has opted in.
//
// The execute bit is what the eBPF uprobe path insists on, and distributions
// ship shared libraries 0644. The bit is not a security boundary for a library
// that is already mapped executable in every process that loads it, so setting
// it is benign; what makes it opt-in is that the file belongs to the package
// manager, which will restore 0644 on the next driver upgrade.
//
// The real file is patched rather than a copy because uprobes are matched by
// inode. A copy would carry the right mode and never fire.
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
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	log.Printf("Tracer: NOTICE: set the execute bit on %s (%#o -> %#o). eBPF uprobes "+
		"require it and no CUDA probe can attach without it. This changes a file owned by "+
		"the NVIDIA driver package; set tracing.fix_libcuda_permissions to false to prevent this.",
		path, info.Mode().Perm(), mode)
	return nil
}

// logAttachments reports what is actually being traced. A driver version that
// renames a kernel symbol silently reduces coverage, so the count of failed
// probes is logged rather than hidden.
func (s *TracerSession) logAttachments(libcuda string) {
	tracer := s.tracer
	log.Printf("Tracer: attached %d of %d program(s)", len(tracer.Attached), len(tracer.Attached)+len(tracer.Failed))

	if len(tracer.Failed) == 0 {
		return
	}

	// Failures are grouped so one actionable line is not buried under fifty
	// copies of itself.
	counts := tracer.FailureCounts()
	causes := make([]string, 0, len(counts))
	for cause := range counts {
		causes = append(causes, cause)
	}
	sort.Slice(causes, func(a, b int) bool {
		return counts[causes[a]] > counts[causes[b]]
	})

	log.Printf("Tracer: %d program(s) did not attach:", len(tracer.Failed))
	for _, cause := range causes {
		log.Printf("Tracer:   %d x %s", counts[cause], cause)
	}
	for _, hint := range attachmentHints(causes, libcuda) {
		log.Printf("Tracer: HINT: %s", hint)
	}
}

// attachmentHints turns a known failure cause into the action that fixes it.
// A cause with no known remedy is left to the grouped list above rather than
// given invented advice.
func attachmentHints(causes []string, libcuda string) []string {
	var hints []string
	for _, cause := range causes {
		if strings.Contains(cause, "is not executable") {
			hints = append(hints, fmt.Sprintf(
				"the CUDA driver library is missing its execute bit, so no CUDA probe can attach; "+
					"run: chmod a+x %s  (uprobes are matched by inode, so the real library must be the one patched)",
				libcuda))
		}
	}
	return hints
}

// drainLoop moves counters from the kernel into the database on a fixed tick.
//
// agg_map is bounded, so a drain that runs too rarely eventually fills it and
// the kernel starts dropping counts. One second keeps the map nearly empty
// without making the database write path chatty, since writes are folded into
// the current minute rather than creating rows per tick.
func (s *TracerSession) drainLoop(ctx context.Context, database *db.DB) {
	ticker := time.NewTicker(tracerTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// One last drain so the counters counted since the previous tick
			// are not lost on shutdown.
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := s.tracer.drainAggregates(flushCtx, database, s.ids, time.Now()); err != nil {
				log.Printf("Tracer: final drain failed: %v", err)
			}
			cancel()
			return

		case at := <-ticker.C:
			if err := s.tracer.drainAggregates(ctx, database, s.ids, at); err != nil {
				log.Printf("Tracer: drain aggregates: %v", err)
			}
			if err := s.tracer.scanHungSyncs(ctx, database, s.ids); err != nil {
				log.Printf("Tracer: scan hung syncs: %v", err)
			}
			if n := malformed.Load(); n > 0 {
				log.Printf("Tracer: %d event(s) could not be decoded; the BPF objects and this binary disagree", n)
				malformed.Store(0)
			}
		}
	}
}

// Stats reports the kernel's own view of whether tracing is keeping up.
func (s *TracerSession) Stats() (Stats, error) {
	return s.tracer.Stats()
}

// Close stops the tracer's background work and releases everything it holds. The
// pinned state maps stay behind on purpose, so a restart keeps which thread
// owned which CUDA context.
func (s *TracerSession) Close() error {
	s.once.Do(func() {
		s.cancel()
		// The ring buffer reader is closed to unblock the consumer, which is
		// otherwise parked in a read that the cancelled context cannot reach.
		if s.tracer.reader != nil {
			s.tracer.reader.Close()
		}
		s.wg.Wait()
		s.tracer.Close()
	})
	return nil
}

// ObjectsExist reports whether a complete set of compiled BPF objects can be
// found, so the daemon can tell "not built" apart from "load failed" instead of
// only logging a permission error.
func ObjectsExist(configured string) bool {
	_, err := resolveObjectsDir(configured)
	return err == nil
}
