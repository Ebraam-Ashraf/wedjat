# proc/test

Verifier load test for `proc_lifecycle.bpf.c`.

This directory compiles the BPF object and feeds it to the kernel verifier via
`load_host_objects`. It does **not** attach the programs or run a behavioral
harness — the proc_lifecycle behavioral regression lives in `kprobes/test`
(`make test TARGET=proc_lifecycle`).

---

## Build & Run

```sh
cd daemon/ebpf/proc/test

make                # compile proc_lifecycle.bpf.o + load_host_objects → build/
sudo make verify    # load the object into the kernel verifier
sudo make test      # same as verify
make clean          # wipe build/
```

---

## What It Builds

| Output | Source |
|---|---|
| `build/proc_lifecycle.bpf.o` | `../proc_lifecycle.bpf.c` (compiled with `common.h` from `../../`) |
| `build/load_host_objects` | `../load_host_objects.c` (standalone libbpf verifier loader) |

---

## What The Test Does

`make verify` runs:

```sh
build/load_host_objects build/proc_lifecycle.bpf.o
```

This opens the `.bpf.o` with `bpf_object__open_file`, then calls
`bpf_object__load` which submits every program to the kernel verifier. On
success it prints `PASS verifier load: build/proc_lifecycle.bpf.o` and exits 0.
On failure it prints the verifier error and exits 1.

It is a **verifier test**, not an attach test. The programs are loaded and
immediately closed — no tracepoints are armed.

---

## Requirements

- Root (`sudo`, `CAP_BPF` + `CAP_PERFMON`)
- Kernel BTF (`/sys/kernel/btf/vmlinux`)
- `clang`, `bpftool`, `libbpf-dev`

No GPU or NVIDIA driver needed — `proc_lifecycle` attaches to scheduler
tracepoints, not NVIDIA symbols.
