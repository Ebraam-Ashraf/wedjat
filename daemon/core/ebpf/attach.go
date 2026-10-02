package ebpf

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
)

// attachAll attaches every program by the kind of section it was compiled
// into, recording failures instead of aborting.
func (t *Tracer) attachAll(libcudaPath string) {
	keys := make([]string, 0, len(t.collection.Programs))
	for key := range t.collection.Programs {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var cuda *link.Executable
	for _, key := range keys {
		prog := t.collection.Programs[key]
		section, ok := t.sections[key]
		if !ok {
			// Not an attachable program, such as the license marker.
			continue
		}

		var (
			l   link.Link
			err error
		)
		switch {
		case strings.HasPrefix(section, "tracepoint/"):
			parts := strings.SplitN(section, "/", 3)
			if len(parts) != 3 {
				t.fail(key, "malformed tracepoint section "+section)
				continue
			}
			l, err = link.Tracepoint(parts[1], parts[2], prog, nil)

		case strings.HasPrefix(section, "uprobe/"):
			if cuda == nil {
				if cuda, err = link.OpenExecutable(libcudaPath); err != nil {
					t.fail(key, err.Error())
					continue
				}
			}
			l, err = cuda.Uprobe(strings.TrimPrefix(section, "uprobe/"), prog, nil)

		case strings.HasPrefix(section, "uretprobe/"):
			if cuda == nil {
				if cuda, err = link.OpenExecutable(libcudaPath); err != nil {
					t.fail(key, err.Error())
					continue
				}
			}
			l, err = cuda.Uretprobe(strings.TrimPrefix(section, "uretprobe/"), prog, nil)

		case strings.HasPrefix(section, "kprobe/"):
			l, err = attachKprobe(prog, strings.TrimPrefix(section, "kprobe/"), false)

		case strings.HasPrefix(section, "kretprobe/"):
			l, err = attachKprobe(prog, strings.TrimPrefix(section, "kretprobe/"), true)
		}

		if err != nil {
			t.fail(key, err.Error())
			continue
		}
		t.links = append(t.links, l)
		t.Attached = append(t.Attached, describe(key, section))
	}
}

func (t *Tracer) fail(key, reason string) {
	t.Failed = append(t.Failed, fmt.Sprintf("%s (%s)", key, reason))
}

// FailureCounts summarises why probes failed to attach, grouped by cause.
//
// A driver that renames a handful of symbols produces a handful of distinct
// messages. A missing execute bit on libcuda produces one message for all fifty
// CUDA probes, and printing those fifty times buries the one line that tells the
// operator what to actually do.
func (t *Tracer) FailureCounts() map[string]int {
	counts := map[string]int{}
	for _, failure := range t.Failed {
		counts[failureCause(failure)]++
	}
	return counts
}

// failureCause reduces a failure line to the part that explains it, dropping the
// symbol name so that every probe failing for the same reason groups together.
func failureCause(failure string) string {
	const marker = " ("
	index := strings.Index(failure, marker)
	if index < 0 {
		return failure
	}
	return failure[index+len(marker) : len(failure)-1]
}

// describe turns a merged program key back into its source attach point.
func describe(key, section string) string {
	if index := strings.IndexByte(key, '/'); index >= 0 {
		key = key[index+1:]
	}
	return section + " " + key
}

// driverSymbolAliases maps a probe's logical name to the kernel symbols that can
// serve it, in preference order.
//
// The NVIDIA driver renames its internal functions between releases, and a
// kprobe on a name that does not exist simply fails to attach. Driver 595
// renames nvidia_ioctl to nvidia_unlocked_ioctl and rewrites the UVM page
// eviction path entirely, so the section name stays the logical name and the
// real symbol is resolved here, against whatever this kernel actually exports.
//
// Only stable names belong in this table. Compiler-generated clones carry
// suffixes like .part.0 or .isra.0 and move between builds, so they are not
// listed: a probe that silently follows a clone is worse than one that fails.
var driverSymbolAliases = map[string][]string{
	"nvidia_ioctl": {
		"nvidia_ioctl",
		"nvidia_unlocked_ioctl",
	},
	"uvm_va_block_evict_pages": {
		"uvm_va_block_evict_pages",
		"uvm_pmm_gpu_pma_evict_pages",
		"uvm_pmm_gpu_pma_evict_pages_wrapper_entry",
	},
	"uvm_va_block_service_fault": {
		"uvm_va_block_service_fault",
		"uvm_va_block_cpu_fault",
		"uvm_va_block_service_locked",
	},
}

// attachKprobe attaches prog to the first symbol that this kernel actually
// exports, so a probe survives a driver rename instead of being dropped.
//
// The aliases for one logical name are tried in order and the last error is
// returned only if every candidate fails, so the message names the symbol the
// daemon actually wanted rather than the first alias it guessed.
func attachKprobe(prog *ebpf.Program, logical string, ret bool) (link.Link, error) {
	candidates, aliased := driverSymbolAliases[logical]
	if !aliased {
		candidates = []string{logical}
	}

	var err error
	for _, symbol := range candidates {
		var l link.Link
		if ret {
			l, err = link.Kretprobe(symbol, prog, nil)
		} else {
			l, err = link.Kprobe(symbol, prog, nil)
		}
		if err == nil {
			return l, nil
		}
	}
	if len(candidates) == 1 {
		return nil, err
	}
	return nil, fmt.Errorf("none of the known names exist for this driver (%s): %w",
		strings.Join(candidates, ", "), err)
}
