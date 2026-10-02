package ebpf

import (
	"debug/elf"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

// bpfObjects are the compiled programs to load.
var bpfObjects = []string{
	"cuda_actions",
	"host_ctx",
	"driver_kprobes",
	"proc_lifecycle",
}

// stateMaps are pinned so a restart keeps which thread owned which CUDA
// context. They must never be pinned with PinByName on two loads at once, so
// they are pinned explicitly after the collection is built.
var stateMaps = []string{"tid_to_device", "ctx_to_device", "alloc_map"}

// defaultPinDir is where the kernel keeps our pinned state, versioned so a
// layout change uses a new directory instead of reading stale state.
const defaultPinDir = "/sys/fs/bpf/wedjat/v1"

// objectDirs are the places the compiled BPF objects may live, most specific
// first. An installed daemon is started by systemd from /, so a path relative
// to the working directory would never resolve there; a developer runs from
// daemon/ where they do.
var objectDirs = []string{
	"/usr/local/lib/wedjat/ebpf",
	"ebpf/build",
}

// libcudaCandidates are the usual locations of the CUDA driver library. The
// versioned soname is the one that carries the symbols.
var libcudaCandidates = []string{
	"/usr/lib/x86_64-linux-gnu/libcuda.so.1",
	"/usr/lib64/libcuda.so.1",
	"/usr/lib/libcuda.so.1",
}

// LoadTracer loads every BPF object into one collection, pins the state maps
// and attaches the programs it can.
//
// Attachment is best effort per program. NVIDIA kernel symbols and CUDA entry
// points change between driver versions, so a probe that does not apply here
// must not stop the probes that do. Callers are expected to log Failed.
func LoadTracer(objectsDir, pinDir, libcudaPath string) (*Tracer, error) {
	merged := &ebpf.CollectionSpec{Maps: map[string]*ebpf.MapSpec{}}
	sections := map[string]string{}

	for _, name := range bpfObjects {
		path := filepath.Join(objectsDir, name+".bpf.o")
		spec, err := ebpf.LoadCollectionSpec(path)
		if err != nil {
			return nil, fmt.Errorf("load spec %s: %w", path, err)
		}

		programSections, err := readProgramSections(path, spec.Programs)
		if err != nil {
			return nil, fmt.Errorf("read sections %s: %w", path, err)
		}

		// A map declared by several objects is kept once, so every program
		// shares one instance instead of splitting its counters.
		for mapName, mapSpec := range spec.Maps {
			if _, exists := merged.Maps[mapName]; !exists {
				merged.Maps[mapName] = mapSpec
			}
		}

		if merged.Programs == nil {
			merged.Programs = map[string]*ebpf.ProgramSpec{}
		}
		for progName, progSpec := range spec.Programs {
			key := name + "/" + progName
			merged.Programs[key] = progSpec
			if section, ok := programSections[progName]; ok {
				sections[key] = section
			}
		}
	}

	// Old kernels charge BPF maps against RLIMIT_MEMLOCK, and the default
	// limit is small enough that loading fails outright. Raising it is a no-op
	// on kernels that account BPF memory to the cgroup instead.
	if err := rlimit.RemoveMemlock(); err != nil {
		log.Printf("tracer: could not raise the memlock limit: %v", err)
	}

	// Pinned state maps are reused when they are already there. A fresh map
	// would start empty, losing which thread owned which CUDA context across a
	// restart, which is the entire reason these maps are pinned.
	pinned, err := loadPinnedState(pinDir, merged)
	if err != nil {
		return nil, err
	}

	collection, err := ebpf.NewCollectionWithOptions(merged, ebpf.CollectionOptions{
		MapReplacements: pinned,
	})
	if err != nil {
		return nil, fmt.Errorf("load eBPF collection: %w", err)
	}

	t := &Tracer{
		collection: collection,
		aggMap:     collection.Maps["agg_map"],
		statsMap:   collection.Maps["stats_map"],
		inflight:   collection.Maps["cuda_inflight_map"],
		sections:   sections,
	}

	if t.reader, err = ringbuf.NewReader(collection.Maps["events_pipe"]); err != nil {
		t.Close()
		return nil, fmt.Errorf("open event ring buffer: %w", err)
	}

	t.pinState(pinDir)
	t.attachAll(libcudaPath)

	if len(t.Attached) == 0 {
		t.Close()
		return nil, errors.New("no eBPF program could be attached")
	}
	return t, nil
}

// attachKinds are the section prefixes that name an attachment point.
var attachKinds = []string{"tracepoint/", "uprobe/", "uretprobe/", "kprobe/", "kretprobe/"}

// readProgramSections maps each program symbol to the ELF section holding it.
// The section name is the attach point: "uprobe/cuMemAlloc_v2" says to probe
// that symbol, "kretprobe/uvm_ioctl" says to probe its return.
//
// Only symbols that are real programs are reported. The table also holds
// clang's local labels, which share the program's section but attach to
// nothing.
func readProgramSections(path string, programs map[string]*ebpf.ProgramSpec) (map[string]string, error) {
	file, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	symbols, err := file.Symbols()
	if err != nil {
		return nil, err
	}

	out := make(map[string]string)
	for _, symbol := range symbols {
		if _, wanted := programs[symbol.Name]; !wanted {
			continue
		}
		if symbol.Section == elf.SHN_UNDEF || int(symbol.Section) >= len(file.Sections) {
			continue
		}
		section := file.Sections[symbol.Section]
		if !hasAttachKind(section.Name) {
			continue
		}
		out[symbol.Name] = section.Name
	}
	return out, nil
}

func hasAttachKind(name string) bool {
	for _, kind := range attachKinds {
		if strings.HasPrefix(name, kind) {
			return true
		}
	}
	return false
}

// loadPinnedState opens the state maps left behind by a previous run so this
// run's programs share them instead of starting from empty maps.
//
// A pinned map whose layout no longer matches this build is ignored rather than
// reused: the versioned pin directory is meant to catch that, and forcing an
// incompatible map in would read the wrong fields as if they were valid.
func loadPinnedState(pinDir string, spec *ebpf.CollectionSpec) (map[string]*ebpf.Map, error) {
	if pinDir == "" {
		return nil, nil
	}
	pinned := map[string]*ebpf.Map{}
	for _, name := range stateMaps {
		path := filepath.Join(pinDir, name)
		if _, err := os.Stat(path); err != nil {
			// Not pinned yet, which is the normal first-run case.
			continue
		}

		m, err := ebpf.LoadPinnedMap(path, nil)
		if err != nil {
			log.Printf("tracer: could not reopen pinned %s, starting it fresh: %v", name, err)
			continue
		}
		mapSpec, ok := spec.Maps[name]
		if !ok {
			m.Close()
			continue
		}
		if err := mapSpec.Compatible(m); err != nil {
			log.Printf("tracer: pinned %s does not match this build, starting it fresh: %v", name, err)
			m.Close()
			continue
		}
		pinned[name] = m
	}
	return pinned, nil
}

// pinState pins the maps that must outlive a restart under a versioned
// directory, so a new daemon build reuses them and an incompatible one does
// not silently read a stale layout.
func (t *Tracer) pinState(pinDir string) {
	if pinDir == "" {
		return
	}
	if err := os.MkdirAll(pinDir, 0700); err != nil {
		log.Printf("tracer: create pin directory: %v", err)
		return
	}
	for _, name := range stateMaps {
		m, ok := t.collection.Maps[name]
		if !ok {
			continue
		}
		path := filepath.Join(pinDir, name)
		// A pin that already exists is the normal case: this map was just
		// reopened from it. Re-pinning it is not an error worth reporting.
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := m.Pin(path); err != nil {
			log.Printf("tracer: pin %s: %v", name, err)
		}
	}
}

// resolveObjectsDir returns the first directory that holds a complete set of
// BPF objects. A partial set is skipped rather than half loaded, since a missing
// object means a whole class of events goes untraced with no error.
func resolveObjectsDir(configured string) (string, error) {
	candidates := objectDirs
	if configured != "" {
		candidates = append([]string{configured}, objectDirs...)
	}

	for _, dir := range candidates {
		if objectsPresent(dir) {
			return dir, nil
		}
	}
	if configured != "" {
		return "", fmt.Errorf("no complete set of BPF objects under %q", configured)
	}
	return "", errors.New("no complete set of BPF objects found; run 'make bpf'")
}

func objectsPresent(dir string) bool {
	for _, name := range bpfObjects {
		if _, err := os.Stat(filepath.Join(dir, name+".bpf.o")); err != nil {
			return false
		}
	}
	return true
}

// findLibcuda returns the first CUDA driver library that exists.
func findLibcuda() (string, error) {
	for _, path := range libcudaCandidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", errors.New("libcuda.so.1 not found; is the NVIDIA driver installed?")
}
