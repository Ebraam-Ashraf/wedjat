package ebpf_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/ebpf"
)

func TestStartTracerDisabledReturnsWithoutLoading(t *testing.T) {
	session, err := ebpf.StartTracer(context.Background(), nil, "", nil, core.TracingConfig{})
	if err != nil {
		t.Fatalf("StartTracer with tracing disabled: %v", err)
	}
	if session != nil {
		t.Fatal("StartTracer with tracing disabled returned a session")
	}
}

func TestStartTracerRejectsIncompleteObjectDirectory(t *testing.T) {
	dir := t.TempDir()
	config := core.TracingConfig{
		Enabled:     true,
		LibcudaPath: "/unused/libcuda.so.1",
		ObjectsDir:  dir,
	}

	_, err := ebpf.StartTracer(context.Background(), nil, "boot-test", nil, config)
	if err == nil {
		t.Fatal("StartTracer accepted an empty object directory")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "object") {
		t.Fatalf("error = %q, want an object-directory error", err)
	}
}

// TestStartTracerMissingLibcudaPath checks that with tracing enabled and no
// LibcudaPath, StartTracer fails with an error naming the missing library.
// It is skipped on machines that have a libcuda, because auto-discovery
// succeeds there and the failure would come from the object directory instead.
func TestStartTracerMissingLibcudaPath(t *testing.T) {
	for _, path := range []string{
		"/usr/lib/x86_64-linux-gnu/libcuda.so.1",
		"/usr/lib64/libcuda.so.1",
		"/usr/lib/libcuda.so.1",
	} {
		if _, err := os.Stat(path); err == nil {
			t.Skipf("libcuda found at %s; skipping missing-path test", path)
		}
	}

	config := core.TracingConfig{
		Enabled:    true,
		ObjectsDir: t.TempDir(), // irrelevant; the libcuda error fires first
	}

	_, err := ebpf.StartTracer(context.Background(), nil, "boot-test", nil, config)
	if err == nil {
		t.Fatal("StartTracer with no libcuda and no driver: expected error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "libcuda") {
		t.Fatalf("error = %q, want a libcuda-not-found error", err)
	}
}
