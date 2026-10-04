package ebpf_test

import (
	"os"
	"path/filepath"
	"testing"

	sourceebpf "github.com/Ebraam-Ashraf/wedjat/daemon/core/source/ebpf"
)

// requiredObjects lists the BPF object file stems that ObjectsExist checks.
// Keep it in sync with the bpfObjects slice in load.go.
var requiredObjects = []string{
	"cuda_actions",
	"host_ctx",
	"driver_kprobes",
	"proc_lifecycle",
}

func TestObjectsExistRequiresCompleteObjectSet(t *testing.T) {
	dir := t.TempDir()

	// Empty directory must be incomplete.
	if sourceebpf.ObjectsExist(dir) {
		t.Fatal("empty object directory reported as complete")
	}

	// Add objects one at a time; only the final addition should flip the result.
	for i, name := range requiredObjects {
		path := filepath.Join(dir, name+".bpf.o")
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		complete := sourceebpf.ObjectsExist(dir)
		isLast := i == len(requiredObjects)-1
		if complete && !isLast {
			t.Fatalf("ObjectsExist returned true after adding only %d/%d objects", i+1, len(requiredObjects))
		}
		if !complete && isLast {
			t.Fatalf("ObjectsExist returned false after adding all %d required objects", len(requiredObjects))
		}
	}
}

func TestObjectsExistRejectsDirectoryWithMissingFile(t *testing.T) {
	dir := t.TempDir()

	// Write all objects except the last one.
	for _, name := range requiredObjects[:len(requiredObjects)-1] {
		if err := os.WriteFile(filepath.Join(dir, name+".bpf.o"), nil, 0644); err != nil {
			t.Fatalf("write partial object set: %v", err)
		}
	}
	if sourceebpf.ObjectsExist(dir) {
		t.Fatalf("partial object directory (%d of %d objects) reported as complete",
			len(requiredObjects)-1, len(requiredObjects))
	}
}

func TestObjectsExistRejectsUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "not-a-bpf-object.txt"), nil, 0644); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}
	if sourceebpf.ObjectsExist(dir) {
		t.Fatal("directory with no complete BPF object set reported as ready")
	}
}
