# Layer 5: Device-Resident GPU Micro-Telemetry

Per-SM block placement, measured from inside the GPU.

`gpu_sm.bpf.c` is a real eBPF program, but it is **not** loaded into the
Linux kernel. Linux eBPF runs on the host CPU and cannot hook a CUDA kernel.
Instead, [bpftime](https://github.com/eunomia-bpf/bpftime) JITs the object to
PTX and injects it into the target CUDA kernel at runtime. The
`kprobe/` / `kretprobe/` section names are bpftime's placeholders for CUDA
kernel entry / exit, and the symbol is the mangled CUDA kernel name.

## What it does

Thread (0,0,0) of each block reports, once at kernel entry and once at exit.
Each record is a `struct dev_event` (see `common.h`) sent through
`dev_events_pipe`. The daemon pairs start and end by ctaid and groups them by
`sm_id` to build the SM heatmap.

`ts_ns` is the GPU globaltimer, not the host clock: the daemon has to
calibrate an offset before joining these events with layers 1-4.

## Limits

- Helper `509` is `bpf_get_sm_id` (reads `%smid`); stock bpftime has it. Helper
  `507` is `asm("exit;")`, so binding it as the smid reader kills the thread
  before any event is written.
- The eBPF **function name** must start with `cuda__`, and the symbol after
  `kprobe/` must not. bpftime decides a program is a GPU program with
  `bpftime_prog::is_cuda()`, which is `name.starts_with("cuda__")` on the
  function name (`runtime/include/bpftime_prog.hpp`); get that wrong and the
  attach is dropped with only a debug-level log, so the test reports no events
  and no error. Conversely the section symbol has to stay the bare mangled
  name: bpftime only strips `cuda_` when `BPFTIME_RUN_WITH_KERNEL` is set, and
  the PTX pass matches the kernel name exactly against the real `.entry`, so a
  prefixed section silently patches nothing.
- Workloads must embed PTX. bpftime extracts PTX from the fatbin, patches it,
  and recompiles; `nvcc -arch=native` alone embeds SASS only, and cuobjdump
  then reports "No PTX file found". `kernels_to_trace/Makefile` adds a
  `-gencode arch=compute_XX,code=compute_XX` for this.
- Only a bounded number of blocks is instrumented. bpftime's GPU ring buffer
  is indexed by *global* thread id, and the write helper drops anything at or
  above `BPFTIME_MAP_GPU_THREAD_COUNT`. `WEDJAT_MAX_GLOBAL_TID` in
  `gpu_sm.bpf.c` caps which blocks report; at 8192 that covers all 32 blocks
  of k1's launch, but instrumenting every block of a 4096-block launch would
  need a ~1M-entry map.
- bpftime's `drain_data()` returns at most one record per ring per call, so
  userspace must poll in a tight loop to keep the rings from filling.
- bpftime only offers kernel entry/exit probes, so "block end" is when thread 0
  finishes, not the whole block: durations are a lower bound.
- The target kernel is one hardcoded mangled name (see the SEC lines).
- bpftime GPU support is experimental. Keep this layer opt-in, out of the
  always-on `wedjatd` path.
- Validated end to end on a real GPU: RTX 3050 Ti, `sm_86`, CUDA 13.4. A passing
  run reports 96 START / 96 END over 32 blocks with `callbacks` matching, so
  the whole chain — CUDA entry/return probe → GPU ring buffer → userspace
  `bpf_map__poll()` callback — works. See `test/README.md` for the run
  procedure and for the one non-obvious requirement: a traced fixture must call
  the exported `cudaLaunchKernel`, not the `<<<>>>` spelling, or bpftime never
  intercepts the launch.

## Other routes for the same data

CUPTI gives SM utilization and stall reasons on the host with no PTX patching,
but no per-block `%smid` placement. It is the fallback if patching bpftime is
not acceptable.

See `test/README.md` for building and running the test.
