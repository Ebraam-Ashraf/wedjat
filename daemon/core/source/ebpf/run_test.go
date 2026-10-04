package ebpf_test

import (
	"context"
	"os"
	"testing"

	sourceebpf "github.com/Ebraam-Ashraf/wedjat/daemon/core/source/ebpf"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

func TestStartDisabledReturnsNil(t *testing.T) {
	cfg := sourceebpf.TracerConfig{Enabled: false}
	chans := source.NewChans(4)
	session, err := sourceebpf.Start(context.Background(), cfg, chans, chans)
	if err != nil {
		t.Fatalf("Start with tracing disabled: %v", err)
	}
	if session != nil {
		t.Fatal("Start with tracing disabled returned a session")
	}
}

func TestStartMissingObjectsReturnsNil(t *testing.T) {
	// An empty directory has no BPF objects — best-effort returns nil, nil.
	dir := t.TempDir()
	cfg := sourceebpf.TracerConfig{
		Enabled:     true,
		LibcudaPath: "/unused/libcuda.so.1",
		ObjectsDir:  dir,
	}
	chans := source.NewChans(4)
	session, err := sourceebpf.Start(context.Background(), cfg, chans, chans)
	// Missing objects is best-effort: session should be nil, err should be nil.
	if err != nil {
		t.Fatalf("Start with empty object directory should return nil,nil: %v", err)
	}
	if session != nil {
		session.Close()
		t.Fatal("Start with empty object directory returned a session")
	}
}

// TestStartMissingLibcuda checks that with tracing enabled and no libcuda
// auto-discoverable, Start returns nil, nil (best-effort).
// Skipped on machines that have a libcuda, because auto-discovery would
// succeed there.
func TestStartMissingLibcuda(t *testing.T) {
	for _, path := range []string{
		"/usr/lib/x86_64-linux-gnu/libcuda.so.1",
		"/usr/lib64/libcuda.so.1",
		"/usr/lib/libcuda.so.1",
	} {
		if _, err := os.Stat(path); err == nil {
			t.Skipf("libcuda found at %s; skipping missing-path test", path)
		}
	}

	cfg := sourceebpf.TracerConfig{
		Enabled:    true,
		ObjectsDir: t.TempDir(),
	}
	chans := source.NewChans(4)
	// No libcuda and no objects → best-effort → nil, nil
	session, err := sourceebpf.Start(context.Background(), cfg, chans, chans)
	if err != nil {
		t.Fatalf("Start with no libcuda: %v", err)
	}
	if session != nil {
		session.Close()
		t.Fatal("Start with no libcuda returned a session")
	}
}
