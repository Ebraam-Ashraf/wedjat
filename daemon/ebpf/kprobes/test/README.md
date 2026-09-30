# kprobes/test

Tests for `driver_kprobes.bpf.c`.

---

## Build & Run

```sh
cd daemon/ebpf/kprobes/test

make                          # build everything → build/
make test                     # build + run driver_kprobes_test (needs sudo + nvidia.ko)
make test TARGET=driver_kprobes  # same thing (explicit)
make test TARGET=proc_lifecycle  # three-worker exit regression (root, no GPU)
make clean                    # wipe build/

# useful variables
make test DUMP=1              # also print every raw event while running
```

---

## What The Test Does

Loads `driver_kprobes.bpf.o` and attaches all kernel probes once.
For every `k*` binary found in `build/`:

1. Fork + exec the binary.
2. Poll `events_pipe` ring buffer while it runs, filtering to that child's pid.
3. Tally events by `api_id` from `enum event_id` in `common.h`.
4. Print one row per binary showing call counts per event type.
5. After all binaries, print a coverage summary — warn about any `EVENT_*`
   id that no binary triggered.

```text
kernel      IOCTL   MMAP   UVM_FAULT   UVM_MIGRATE   UVM_EVICT   result
k1              20      1            0             0           0   ok
k2               5      3            0             0           0   ok
k3              24      4            1             2           1   ok
```

Tracked events: `EVENT_IOCTL`, `EVENT_UVM_IOCTL`, `EVENT_MMAP`,
`EVENT_UVM_FAULT`, `EVENT_UVM_MIGRATE`, `EVENT_UVM_EVICT`.

Probes and the events they emit:

```text
kprobe + kretprobe  nvidia_ioctl               -> EVENT_IOCTL
kprobe + kretprobe  uvm_ioctl                  -> EVENT_UVM_IOCTL
kprobe + kretprobe  nvidia_mmap                -> EVENT_MMAP
kprobe + kretprobe  uvm_va_block_service_fault -> EVENT_UVM_FAULT
kprobe + kretprobe  uvm_migrate                -> EVENT_UVM_MIGRATE
kprobe + kretprobe  uvm_va_block_evict_pages   -> EVENT_UVM_EVICT
```

---

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `DUMP` | unset | Print every raw `struct event` while running |

---

## Requirements

- Root (`sudo`, `CAP_BPF` + `CAP_PERFMON`)
- NVIDIA driver + GPU (`/dev/nvidiactl`)
- `nvidia.ko` and `nvidia-uvm.ko` loaded
- `clang`, `bpftool`, `libbpf-dev`

Missing root or GPU → exit `77` (SKIP), not `FAIL`.

---

## Adding a New Probe

```text
driver_kprobes.bpf.c    add the SEC("kprobe/new_symbol") probe
driver_kprobes_test.c   add the EVENT_* id to TRACKED_IDS[] and TRACKED_NAMES[]
                        add new_symbol to hooks[]
```

## Process lifecycle regression

The proc_lifecycle_test starts a process with three worker threads, lets those workers exit while the main thread remains alive, and asserts that no process-exit event is emitted yet. It then exits the main thread and requires exactly one process-exit event. This catches an off-by-one signal->live check.
