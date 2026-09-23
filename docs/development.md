# Wedjat Development Workflow

This project should be developed in layers. Do not wait for the UI, database, or
all five telemetry layers before testing. Each layer needs its own cheap checks
and its own real integration test.

## The Test Ladder

Use four levels of tests.

### 1. Cheap Static Checks

These should run on every commit and pull request.

They answer:

- Are files formatted consistently?
- Is there trailing whitespace?
- Do required docs/checklists exist?
- Later: do BPF C files compile to object files?
- Later: does the user-space loader compile?

Command:

```sh
make check
```

These checks do not require a GPU, root, CUDA, or NVIDIA drivers. They are the
CI baseline.

### 2. BPF Build/Verifier Checks

These are the first real eBPF checks once implementation starts.

They answer:

- Can clang compile the `.bpf.c` file for the BPF target?
- Can libbpf/bpftool load the object?
- Does the kernel verifier accept the program?
- Are map definitions and struct layouts valid?

This still does not prove the CUDA hook fires. It only proves the BPF program is
valid enough for the kernel to load.

Future command shape:

```sh
make bpf-build
make bpf-load-check
```

Expected future artifacts:

```text
daemon/ebpf/build/host_ctx.bpf.o
daemon/ebpf/build/cuda_actions.bpf.o
daemon/ebpf/build/driver_kprobes.bpf.o
```

### 3. Uprobe Integration Tests

This is the important test for Layer 1 and Layer 2.

They answer:

- Can the loader find `libcuda.so`?
- Does the chosen symbol exist, such as `cuLaunchKernel`?
- Can the loader attach a uprobe/uretprobe?
- Does running a CUDA workload produce a BPF event?
- Does the event contain the expected PID/TID/comm/cgroup/cpu/timestamp?

This usually needs:

- NVIDIA driver installed
- CUDA workload binary
- permissions for BPF/perf/uprobe attachment
- sometimes root or capabilities such as `CAP_BPF`, `CAP_PERFMON`, `CAP_SYS_ADMIN`

Future command shape:

```sh
make test-uprobes
```

Do not run this in normal GitHub Actions unless the runner has a GPU and the
right kernel privileges. Keep it as a local or self-hosted CI job.

### 4. Real GPU Scenario Tests

These use `kernels_to_trace` workloads. They are not unit tests; they are
controlled workloads that intentionally trigger probe surfaces.

They answer:

- Does Layer 1 see CUDA processes reliably?
- Does Layer 2 see launch/malloc/memcpy/sync activity?
- Does Layer 3 see driver ioctl/UVM activity when present?
- Can the daemon join BPF events with NVML device snapshots?

Future command shape:

```sh
make test-gpu
```

These tests can be slow and machine-dependent. They belong in a local developer
workflow or a self-hosted GPU CI runner, not ordinary GitHub-hosted CI.

## Layer-Specific Development Order

### Layer 1: Host Identity

First milestone:

```text
Attach uprobe to cuLaunchKernel.
Run one CUDA workload.
Emit one event with pid/tid/comm/cgroup/cpu/timestamp.
Assert event PID matches the child workload PID.
```

Do not parse CUDA launch arguments yet. Do not build the UI yet. Do not add
SQLite yet.

### Layer 2: CUDA Intent

After Layer 1 is reliable:

```text
Add cuMemAlloc entry/return pairing.
Add cuStreamSynchronize entry/return pairing.
Add cuMemcpyAsync counters.
Add cuLaunchKernel dimensions.
```

Use inflight maps keyed by `pid_tgid` for entry/return pairing.

### Layer 3: Driver/OS

After Layer 1/2 work:

```text
Discover NVIDIA kernel symbols at runtime.
Attach only the symbols that exist on this host.
Aggregate high-volume hooks like ioctl.
Emit ringbuf events only for slow/failing/notable cases.
```

Never assume every driver version exposes the same internal UVM function names.

### Layer 4: NVML

Develop as ordinary user-space C.

```text
Poll device state every second.
Record device index + UUID.
Record utilization/memory/power/temp/process list.
Join with BPF events by timestamp bucket and PID/device.
```

NVML does not use `SEC(...)`.

### Layer 5: Device Micro-Telemetry

Keep this separate until Layers 1-4 are boring.

Normal host eBPF does not attach inside CUDA kernels. For this layer, evaluate
CUPTI before attempting PTX/SASS injection.

## CI Strategy

Use two CI classes.

### GitHub-Hosted CI

Runs cheap checks only:

```text
make ci
```

Allowed:

- formatting
- whitespace
- docs/checklist existence
- later, pure compile checks that do not require kernel privileges

Not allowed:

- loading BPF programs
- attaching uprobes
- requiring NVIDIA GPUs
- requiring `/proc/kallsyms` access

### Self-Hosted GPU CI

Runs privileged integration tests:

```text
make test-uprobes
make test-gpu
```

Requires:

- NVIDIA GPU
- CUDA driver/runtime
- kernel headers/vmlinux BTF
- permissions for BPF and perf events
- known-good test workloads from `kernels_to_trace`

## Coding Style

Use the repository `.clang-format` for C/eBPF/NVML files.

General rules:

- Keep Layer 1 identity helpers shared and boring.
- Keep high-frequency events aggregated by default.
- Use ringbuf for dev visibility, errors, long stalls, and rare events.
- Keep driver-version-sensitive kprobes optional at runtime.
- Treat CUDA/NVML/driver absence as skip conditions in tests, not crashes.

