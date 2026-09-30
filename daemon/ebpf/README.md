# eBPF collection

The host programs share the ABI and map definitions in `common.h`:

- `uprobes/cuda_actions.bpf.c` captures CUDA allocation/free counters, copy/launch/sync activity, and tracks allocations by process and device ordinal.
- `uprobes/host_ctx.bpf.c` tracks CUDA context-to-device and thread-to-device state.
- `kprobes/driver_kprobes.bpf.c` captures NVIDIA ioctl/mmap and UVM activity. NVIDIA kernel symbols and argument layouts are driver-version dependent; failed optional attachments must be surfaced by userspace.
- `proc_lifecycle.bpf.c` emits exec/last-thread exit events for processes previously seen producing GPU events, with the process start-boottime identity and kernel exit status. The last-thread check expects `signal->live == 0` at `sched_process_exit`, after the kernel decrements the count.
- `gpu/gpu_sm.bpf.c` is a separate bpftime GPU-side object. Its custom map type is not a kernel map and must not be passed to the host-kernel verifier.

`struct event` is a 64-byte host event. `device_ordinal` means the CUDA-visible ordinal in that process, not a physical GPU index or NVML index. Userspace must resolve it to a GPU UUID using the process environment and NVML before storing device identity. `start_boottime_ns` is nanoseconds; normalize `/proc/<pid>/stat` starttime ticks to nanoseconds using that system's `CLK_TCK` before constructing process identity. `alloc_map` supports alloc/free byte counters only; NVML remains the VRAM usage authority. On process exit, userspace must delete all `alloc_map` and `ctx_to_device` entries for that tgid.

## Loading contract

A production loader should own one `cilium/ebpf` collection and attach the programs from that collection, using `MapReplacements` for identically defined maps. State maps intended to survive daemon restarts are `tid_to_device`, `ctx_to_device`, and `alloc_map`; pin them under a versioned location such as `/sys/fs/bpf/wedjat/v1/`. Do not pin `agg_map` or `events_pipe`. Verify map layouts before reusing pins, and use a new version directory when a layout changes. Startup must sweep stale pinned state, re-seed `seen_processes` for live tracked jobs, and validate per-process device state against process start identity.

The daemon must drain `agg_map` once per second using `LookupAndDeleteBatch`; otherwise its bounded 8192-key capacity eventually fills. Sum per-CPU counters, but compute `latency_max_ns` as the maximum across CPU slots. Consume `stats_map`, drain `events_pipe`, delete per-tgid context/allocation state on process exit, and snapshot NVML VRAM independently. For completed sync calls, `sync_stall_us` emits an incident event when elapsed latency crosses the threshold. Since a hung sync never returns, scan `cuda_inflight_map` once per second for old `EVENT_SYNC` entries and raise a hang incident; the BPF return probe cannot report a call that never returned.

This directory contains the BPF programs and focused tests, not the production Go loader. The loader and persistence integration remain a daemon-layer task; database and SQL files are intentionally outside this eBPF pass.

## Tests

From `daemon/ebpf/test`, `make build` compiles all four host objects and the libbpf loader test. `sudo make verify` attempts to load each object separately into the running kernel and fails on the first verifier/load error. This requires kernel BTF, libbpf development files, and BPF loading privileges. It is a verifier/load test, not an attach test. The bpftime GPU object is built and exercised separately by `gpu/test`.

Uprobe and kprobe behavior harnesses live under `uprobes/test` and `kprobes/test`; GPU behavior tests live under `gpu/test`. Their fixture runs require the matching CUDA/NVIDIA/bpftime environment.
