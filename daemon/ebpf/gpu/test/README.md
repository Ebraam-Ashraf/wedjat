# gpu/test

Tests for `gpu/gpu_sm.bpf.c` (Layer 5).

**Validated on a real GPU**: RTX 3050 Ti, `sm_86`, CUDA 13.4, driver-supplied
`compute_86` PTX. A passing run reports 96 START / 96 END over 20 distinct SMs,
which is 32 blocks of k1's launch across 3 iterations.

---

## Build & Run

```sh
cd daemon/ebpf/gpu/test

make                          # build everything → build/
make test                     # build + run gpu_sm_test (needs sudo + bpftime)
make repeat                   # 20 consecutive runs + a pass/fail tally
make clean                    # wipe build/

# useful variables
make test BPFTIME_SERVER=/path/libbpftime-syscall-server.so
make test BPFTIME_AGENT=/path/libbpftime-agent.so
make test DUMP=1                     # print every matching event as it's polled
make test BPFTIME_LOG=console         # bpftime's own log, on stderr
make test LIBBPF_DEBUG=1             # libbpf's section/relocation trace
make repeat REPEAT=50
```

### Output

A passing run looks like `cuda_actions_test` and `host_ctx_test`:

```text
== gpu_sm ==
load    ok
attach
  2/2 attached

kernel          START         END    SMs  result
WEDJAT_FIXTURE name=k1_basic_launch n=8192 bytes=32768 iters=3
WEDJAT_FIXTURE_EVENT name=k1_basic_launch iter=0 grid=32 block=256
WEDJAT_FIXTURE_DONE name=k1_basic_launch sample=2.500450
  k1                  96          96     20  ok
WEDJAT_FIXTURE name=k2_tiled_matmul M=2048 iters=3
WEDJAT_FIXTURE_DONE name=k2_tiled_matmul C[0,0]=2.455596 PASS
  k2                   .           .      .  n/a (target kernel not in this binary)
WEDJAT_FIXTURE name=k4_bracket_launch n=8192 iters=3
WEDJAT_FIXTURE_DONE name=k4_bracket_launch sample=2.500450
  k4                   .           .      .  XFAIL (target present, launch not intercepted)

coverage
  ok    START
  ok    END

gpu_sm_test: PASS
```

The row format is `cuda_actions_test`'s verbatim — same `%-10s` name, same
`%10s` per tracked event, `.` for zero — with one extra column for distinct
SMs. `cuda_actions_test` reports LAUNCH/ALLOC/FREE/MEMCPY/SYNC where this
reports START/END.

`stdout` is explicitly line-buffered in `main`. The Makefile redirects it to a
file, and files are block-buffered by default, which would leave the parent's
rows sitting in a buffer until exit while the forked children wrote straight
through — every fixture's output first, every result row last. Line buffering
keeps a row next to its own fixture.

Layer 5 patches PTX at runtime, which the two uprobes tests never do, so
bpftime emits two kinds of output that ignore `BPFTIME_LOG_OUTPUT`:

- ptxas is a subprocess and prints `Extracting PTX file and ptxas options`
  straight to our stdout
- the ptxpass plugin `.so` installs its own spdlog logger before
  `bpftime_set_logger` runs, so `[ptxpass] ... matched=N` and the
  `bpftime_vm_compat` warning land on stderr as well

Both are spdlog/ptxas lines — they start with a timestamp or the word
`Extracting`, and the test's own output never does. By default the Makefile
filters those lines from the live terminal while preserving all child output
in `build/test.log`; bpftime's own logger writes to `build/bpftime.log`:

```sh
grep -E 'ptpass|Copying map fd|Attach successfully' build/bpftime.log
```

`[ptxpass] ... matched=N` means the probe was injected into the PTX,
`Copying map fd` means the ring reached the GPU, `Attach successfully` means
the child took it. `make test BPFTIME_LOG=console` streams the unfiltered child
output, including bpftime's own narration.

`make repeat` prints a pass/fail tally without writing numbered per-run logs. The last run's detailed output remains in `build/test.log`, and bpftime diagnostics remain in `build/bpftime.log`.

libbpf's own debug stream is off unless `LIBBPF_DEBUG=1`; libbpf *warnings* are
always shown, since that is how a rejected object announces itself.

`gpu_sm_test` has to run under bpftime, so it skips (exit 77) on any
machine that doesn't have it preloaded:

```sh
sudo LD_PRELOAD=$HOME/.bpftime/libbpftime-syscall-server.so \
     ./build/gpu_sm_test ../../../../kernels_to_trace/build
```

---

## What Each Test Does

### `gpu_sm_test`

Same shape as `driver_kprobes_test.c` / `cuda_actions_test.c` /
`host_ctx_test.c` — load once, attach once, loop every `k*` binary, print one
row, then a coverage block — with three differences that come from layer 5
living on the GPU:

1. **The object is not loaded into the Linux kernel.** bpftime JITs it to PTX
   and injects it into the target CUDA kernel, so the test process must run
   with bpftime's syscall server preloaded, and each child must run with
   bpftime's agent preloaded (that is what the test sets in the child).
   Without either it exits `77` (SKIP) before it loads anything.
2. **Both probes are bound to one mangled kernel name** — currently k1's
   `scale_add_kernel`, `_Z16scale_add_kernelPfffi`, in the `SEC()` lines of
   `gpu_sm.bpf.c`. A `k*` binary that doesn't contain that kernel correctly
   produces nothing, so zero events on a row is `n/a`, not a failure. The test
   only fails if *no* binary produced events.
3. **Block events arrive while the workload runs**, so the ring buffer is
   polled during the run (the same `waitpid(WNOHANG)` loop `host_ctx_test.c`
   uses), not once after it exits.

For every `k*` binary found in `build/`:

1. Fork + exec it, with the agent preloaded.
2. Poll `dev_events_pipe` while it runs, filtering to that child's pid.
3. Tally `EVENT_SM_BLOCK_START` / `EVENT_SM_BLOCK_END`, and collect the set of
   SM ids seen.
4. Pair each `START` with the next `END` **per `ctaid`**, as a small state
   machine, and check the invariants below.
5. Print one row: start count, end count, distinct SMs, result.
6. After all binaries, print a coverage summary.

### Per-block pairing

Each `ctaid` keeps `launches`, `open`, `start_sm`, `start_ts` and a one-shot
`problem` string. `START` opens a launch, `END` closes it, and the checks are
per *pair*:

- the block must not migrate SM mid-launch (`END.sm_id == START.sm_id`)
- `END.ts_ns >= START.ts_ns`
- no `END` without a `START`, and no `START` still open when the child exits
- `launches == WEDJAT_ITERS` — every block reports once per launch

Two things this deliberately does **not** do, both of which were bugs in an
earlier version that failed `k1` on a perfectly healthy run:

- **It does not require one SM per `ctaid` across the whole run.** The same
  block index runs once per launch and the scheduler may place it on a
  different SM each time. Only START and END of the *same* launch must agree.
- **It does not store `ts_ns` in a `u32`.** It is a 64-bit globaltimer in
  nanoseconds and wraps a `u32` every ~4.3 s, which silently disabled the
  ordering check.

Pairing relies on each block's ring being FIFO. That holds because the ring is
indexed by global thread id and only thread `(0,0,0)` of a block writes, so one
block's records share one ring in program order. If `DUMP=1` ever shows
START/END interleaved, buffer per `ctaid` and sort by `ts_ns` instead.

Sample output from a real run, abridged (k1 only; k2 through k4 are elided):

```text
kernel          START         END    SMs  result
  k1                  96          96     20  ok

coverage
  ok    START
  ok    END

gpu_sm_test: PASS
```

Row failures: the workload exiting non-zero, a `ts_ns` of zero, an `sm_id`
out of range, a start count that doesn't match the end count, or events paired
with no SM seen at all. Note that a bare `start == end` also passes `0 == 0`,
so a row only counts as `ok` when it actually saw events.

A row with no events is not necessarily a fault, but which of the three it is
now matters, so the test decides from the **binary itself** rather than from
what the other rows did. It scans each fixture for `WEDJAT_TARGET_SYM`
(defined once in `daemon/ebpf/common.h`, used by the `SEC()` names and by this
check) and labels the row accordingly:

| row | meaning | verdict |
| --- | --- | --- |
| `n/a (target kernel not in this binary)` | `k2`/`k3` have no `scale_add_kernel`; nothing to trace | correct |
| `XFAIL (target present, launch not intercepted)` | `k4` has it, but its `<<<>>>` launch never reaches the probe — see [k4 exists to keep this honest](#k4-exists-to-keep-this-honest) | known gap, does not fail the suite |
| `XPASS (now traced: ...)` | `k4` started producing events; bpftime grew a `__cudaLaunchKernel` hook | remove it from `known_untraced()` and update this file |
| `FAIL(target present, no events reached the poll callback)` | `k1` has the kernel and produced nothing | real failure |

That last row is the one that used to be invisible. The old logic asked
"did any *other* fixture produce events?", which cannot tell a kernel that is
absent from one that is present but untraced — so a broken poll path on `k1`
printed a reassuring `n/a`.

A `start != end` on a kernel that clearly has blocks is ambiguous: it is
either a real pairing bug, or the GPU ringbuf filled up and dropped the tail.
There is no drop counter in `gpu_sm.bpf.c`, so the test cannot tell the two
apart yet — check `dev_events_pipe` sizing before believing the first one.

### `build-bpf`

`gpu_sm.bpf.c` is compiled with `clang -target bpf` and a skeleton is
generated for it, exactly like the host probes. The target also asserts two
things about the object itself:

1. both `kprobe/` and `kretprobe/` sections are present
2. the host maps (`events_pipe`, `inflight_map`, `tid_to_device`,
   `ctx_to_device`) are **absent** — `DEVICE_BPF` in `common.h` `#if`s them
   out, and this check is what keeps them out

```text
sections in build/gpu_sm.bpf.o:
  3 kprobe/_Z16scale_add_kernelPfffi        00000170 0000000000000000 TEXT
  4 .relkprobe/_Z16scale_add_kernelPfffi    00000010 0000000000000000
  5 kretprobe/_Z16scale_add_kernelPfffi     00000170 0000000000000000 TEXT
  6 .relkretprobe/_Z16scale_add_kernelPfffi 00000010 0000000000000000
  8 .maps                                   00000010 0000000000000000 DATA
PASS build-bpf (both probes, host maps absent)
```

The `cuda__` prefix on the **function** name is required, not decorative:
bpftime treats a program as GPU-resident only if `bpftime_prog::is_cuda()` sees
the function name start with `cuda__`. Without it the attach is dropped at
debug log level, so the run looks like it attached and then produced nothing.

The section symbol deliberately does **not** carry the prefix. bpftime strips
`cuda_` from a probe name only when `BPFTIME_RUN_WITH_KERNEL` is set
(`syscall_context.cpp:856`), and the PTX pass matches the kernel name exactly
against the real `.entry` name, so a prefixed section makes it look for a
kernel that does not exist and patch nothing.

`bpf_get_sm_id` is helper `509` in stock bpftime (helper `507` is `asm("exit;")` —
using it kills the thread before the event is written). `bpf_get_block_idx`,
`bpf_get_block_dim` and `bpf_get_thread_idx` ship with bpftime.

---

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `BPFTIME_SERVER` | `$HOME/.bpftime/libbpftime-syscall-server.so` | `LD_PRELOAD` target for the test process |
| `BPFTIME_AGENT` | `$HOME/.bpftime/libbpftime-agent.so` | `LD_PRELOAD` target for each child |
| `DUMP` | unset | Print every raw `struct dev_event` while running |
| `ARCH` | from `uname -m` | BPF target arch, derived the same way as the other test dirs |

### Telling "probe never ran" from "event never arrived"

`callbacks=0` on its own is ambiguous. See
[The execution witness](#the-execution-witness-what-it-does-and-does-not-prove)
for how the test separates the two, and why a single on/off comparison is not
enough.

### What the GPU side does with the event

`bpf_perf_event_output` is helper 25. On the GPU it is **not** a stub: the
device implementation is `_bpf_helper_ext_0025` in bpftime's
`attach/nv_attach_impl/trampoline/default_trampoline.cu`, and it explicitly
handles our map type:

```cpp
const auto &map_info = ::map_info[map];
if (map_info.map_type == BPF_MAP_TYPE_GPU_RINGBUF_MAP) {
        auto tid = getGlobalThreadId();
        if (tid >= map_info.max_thread_count) return 1;   // OOB guard
        auto header = (ringbuf_header *)(tid * entry_size +
                          (char *)map_info.extra_buffer);
        ...
} else {
        printf("Calling bpf_perf_event_output on unsupported map!");
        return 1;
}
```

(Note that `core.cpp` in ptxpass registers helper 25 to a `test_func` that
returns 0. That is the compile-time stub, not what runs on the device — don't
read it as "helper 25 is unimplemented".)

Three consequences worth knowing:

- The ring is indexed by **global thread id**, so `BPFTIME_MAP_GPU_THREAD_COUNT`
  must be at least `WEDJAT_MAX_GLOBAL_TID` or the probe is rejected by the
  bounds check and returns 1 silently.
- If `map_info.map_type` is not `1527`, the device prints
  `Calling bpf_perf_event_output on unsupported map!` to stdout and drops the
  event. **That string in the output is the single clearest failure signal.**
- `extra_buffer` must hold a *device* pointer. It comes from
  `cuMemHostGetDevicePointer` in `nv_gpu_ringbuf_map.cpp`, per-pid.

### Reading the poll

`drain_data` (`nv_gpu_ringbuf_map.cpp:56`) walks each per-thread ring, skips
any ring with the `dirty` flag set (logging `Ignored dirty pages`), and emits
at most **one** record per ring per call. The `bpf_map__poll()` wrapper in
`map_handler.cpp` returns as soon as it drains one, so the test must poll in a
tight loop — which it does. `calls=2190467` with `callbacks=0` means the loop
is running and genuinely nothing was ever in a ring.

### Diagnostic logging

The `test` target sets `BPFTIME_LOG_OUTPUT=console SPDLOG_LEVEL=info`. Four
lines pin the failure to a specific hop:

| Log line | Source | Meaning if absent |
|----------|--------|-------------------|
| `Copying map fd 16 to device, ... map_type = 1527, gpu_buffer = 0x...` | `bpf_attach_ctx_cuda.cpp:345` | the map never reached the device — `extra_buffer` is null and every write is garbage |
| `Mapped GPU memory for gpu ringbuf map: 0x...` | `nv_gpu_ringbuf_map.cpp:46` | `cuMemHostGetDevicePointer` failed in the child |
| `Ignored dirty pages` | `nv_gpu_ringbuf_map.cpp:65` | a ring was mid-write; the device died or desynced inside the record copy |
| `Calling bpf_perf_event_output on unsupported map!` (stdout) | `default_trampoline.cu:408` | helper 25 ran but rejected the map |

A healthy attached child logs the first two and neither of the last two. If
that holds and the ring is still empty, the loss is on the host poll side.

### Dumping the ring directly

The ring lives inside `/dev/shm/bpftime_maps_shm`, which survives the run, so
it can be inspected without any instrumentation. The child's log gives the
shm base (`Registered shared memory with CUDA: addr=0x...`) and the ring
address (`gpu_buffer = 0x...`); the difference is the ring's offset:

```sh
sudo python3 - <<'E'
d = open('/dev/shm/bpftime_maps_shm','rb').read()
r = d[0x2fccf0:0x2fccf0 + 0xC0000]      # 1024 rings x 16 x 40B, plus headers
print(sum(1 for b in r if b), "nonzero bytes of", len(r))
E
```

- **0 nonzero bytes** — the device never wrote. Look upstream, at launch
  redirection or the `tid >= max_thread_count` guard.
- **Nonzero bytes** — the device wrote and the host poll is not returning the
  records. Check the `dirty` flag and head/tail handling.

---

## The loading process must not fork without exec

Loading a `1527` map makes the **loading** process a CUDA process: bpftime
calls `cuDeviceGet` + `cuCtxCreate` + `cuMemHostGetDevicePointer` in
`map_init` (`handler/map_handler.cpp`, `bpf_map/gpu/nv_gpu_ringbuf_map.cpp`).
What may follow that is not free:

| Pattern | Result | Measured |
|---------|--------|----------|
| `map_init` twice, no fork | works | 3/3 |
| `map_init`, `fork`, use CUDA in child | **child's first CUDA call fails** (`cuDeviceGet` → `initialization error`) | 6/6 |
| `map_init`, `fork`, `execve` in child | works | 5/5 |

(Measured with a standalone `.cu` that replays bpftime's `map_init` sequence —
`cuDeviceGet` + `cuCtxCreate` + alloc + `cuMemHostGetDevicePointer` in the
parent, then the same in the child. It needed neither root nor bpftime and was
removed once the table above was established; recreate it from this description
if you need to re-measure on a new driver or CUDA version.)

`gpu_sm_test` loads in the parent and has each child `execve` the workload,
which is the row that works. A test that merely `fork()`ed and ran CUDA in the
child would fail exactly as above, and the failure looks like a bpftime bug
rather than a fork bug.

## Stale `/dev/shm` makes a crashed run poison every run after it

bpftime keeps its handler and map tables in `/dev/shm/bpftime_maps_shm` and
never unlinks it. The server creates it with boost's `create_only`; if it
already exists, the server falls back to `open_or_create`
(`runtime/syscall-server/syscall_server_utils.cpp:126`). `open_or_create`
**does not resize an existing segment**, so the next run reuses the old one
*including* its `handler_manager` — stale map entries and device pointers
belonging to a process that no longer exists.

That is the whole mechanism behind the crash that used to stop this test dead
at `gpu_sm_bpf__open_and_load()`, before `attach()` and before any CUDA
workload ran:

```text
== gpu_sm ==
Segmentation fault
```

It looks nondeterministic because it is a function of history, not of the
object. A fresh boot loads once, then that same run dies somewhere else, and
every subsequent run inherits the corpse. The `test` target now removes the
segment before every run; if you invoke `gpu_sm_test` by hand, do the same:

```sh
sudo rm -f /dev/shm/bpftime_maps_shm
```

Two things that are *not* the cause, both worth recording so they are not
re-litigated:

- **The map declaration.** The run that reached `load ok` used a *larger*
  object (`.maps 00000048`, two maps, 20MB ring) than the runs that crashed
  (`.maps 00000020`, one map). The bigger one loaded fine.
- **Dropping `__type(key)` / `__type(value)`.** This is a regression, not a
  diagnostic. libbpf only fills those in automatically for the kernel's own
  ringbuf type, so without them map `1527` loads with value size 0 and the
  probe silently records nothing.

## Root cause of the long `callbacks=0` run

Worth recording because it is not guessable and the logs actively point away
from it. The probe (`gpu_sm.bpf.c`) was correct the whole time, and so was
bpftime: the patched-PTX hashes and the bpftime `.so` files are byte-identical
across the failing and passing runs. Only `kernels_to_trace/k1.cu` changed.

`scale_add_kernel<<<grid, block>>>(...)` does **not** call `cudaLaunchKernel`.
In `cuda_runtime.h` that spelling is a `static __inline__` template, so nvcc
inlines it and the real dynamic call becomes `__cudaLaunchKernel`:

```text
$ nm -D --undefined-only build/k1
                 U __cudaLaunchKernel@libcudart.so.13
```

bpftime intercepts launches by replacing the *exported* symbols
`cudaLaunchKernel`, `cudaLaunchKernel_ptsz` and `cuLaunchKernel`
(`attach/nv_attach_impl/nv_attach_impl.cpp`, via `replace_hook_once`). It never
replaces `__cudaLaunchKernel`. So with the `<<<>>>` spelling the patched module
was built, compiled, cached and loaded — and then **never launched**. Every log
line looked healthy, ptxpass reported `matched=1`, the PTX grew
`917 -> 5386 -> 9861`, and no probe ran and no event was ever produced.

Both symbols do exist in libcudart, so the hook target is reachable:

```text
$ nm -D --defined-only libcudart.so | grep cudaLaunchKernel
0000000000025cc0 T __cudaLaunchKernel@@libcudart.so.13
00000000000855e0 T cudaLaunchKernel@@libcudart.so.13
```

`k1.cu` therefore declares the exported one by hand and calls it through a
function pointer, so it binds to the symbol bpftime hooks while going through
the identical C runtime ABI underneath. After the change:

```text
$ nm -D --undefined-only build/k1 | grep -i launch
U __cudaLaunchKernel@libcudart.so.13
U cudaLaunchKernel@libcudart.so.13     <-- bpftime's hook target
$ ./build/k1   ->  sample=2.500450     (unchanged, still numerically correct)
```

### `k4` exists to keep this honest

Making `k1` call a function pointer by hand means the only traced fixture is a
hand-written one, which raises an obvious question: does an **unmodified** CUDA
program get traced at all? `k4_bracket_launch` answers it on every run. It is
byte-for-byte the same kernel, the same signature, the same grid and block as
k1 — the only difference is that it launches with `<<<>>>`. So:

```text
$ nm -D --undefined-only build/k4 | grep -i launch
U __cudaLaunchKernel@libcudart.so.13     <-- and nothing else
```

| Fixture | Launch spelling | Imports `cudaLaunchKernel` | Traced |
|---------|-----------------|----------------------------|--------|
| `k1` | explicit symbol call | yes | yes |
| `k4` | `<<<>>>` | no | **no** |

On a stock bpftime, **k4 reports no events**, and that is the expected result,
not a bug in the test. Read the k4 row in the test output as the standing
answer to "does this work on real applications".

**It does not, yet.** An ordinary CUDA program compiled from NVIDIA's headers
goes through `__cudaLaunchKernel`, so bpftime never intercepts the launch and
the probe never fires. Until bpftime hooks that symbol too, Layer 5 only works
for a target that calls the exported `cudaLaunchKernel` itself. This is the
single biggest constraint on the layer and it belongs in the daemon design, not
just here: the fix is a one-line addition to bpftime's `replace_hook_once` list
(next to `cudaLaunchKernel_ptsz` and `cuLaunchKernel`), or a bpftime change to
intercept at the fatbin/registration level instead of the launch level.

### There is no wall-clock witness, on purpose

The test used to compare a hooked child against a bare one and read the
difference as "the probe ran". That number was always wrong, and the error
changed shape as the test was fixed. Three separate costs land inside it:

- bpftime's own bootstrap — cuobjdump, ptxas, module load, late attach — paid
  only by the hooked child;
- an extra ptxas compile and patch round, paid only by a kernel that actually
  *matches* the probe, because matching both probes means patching twice (the
  log shows `Start compiling ... not found in cache` twice for k1, once for
  k3);
- the fixture's own `WEDJAT_SLEEP_MS` sleep, ~400ms in k1.

No subtraction of one reference point removes the other two. One revision
reported 66ms and called it proof; the next reported 642ms of which most was
sleep. Both were noise dressed as a measurement. The block is deleted rather
than tuned, because `callbacks > 0` already proves the probe runs, and when it
ever drops to zero the log is the thing to read, not a clock.

## `Ignored dirty pages` warnings are expected

A busy run logs a few `Ignored dirty pages` per kernel launch, from the syscall
server, e.g. 24 during a 3-iteration k1 run. This is normal. `drain_data`
(`nv_gpu_ringbuf_map.cpp:56`) walks the per-thread rings and bails out on the
first ring whose `dirty` flag is set — the device sets that flag around the
record copy to mark a ring mid-write. With the poll spinning millions of times
per second it will keep catching rings in that window; the next poll picks them
up. The warning means "skipped this ring *this time*", not "dropped data".

Caveat: `drain_data` returns `0` on the first dirty ring, so it skips the
*remaining* rings in that pass too. With enough rings in flight that could in
principle starve one ring, so the flakiness check is worth running.

---

## Requirements

`make` and `build-bpf` (no GPU, no root):

- `clang`, `bpftool`, `libbpf-dev`, readable `/sys/kernel/btf/vmlinux`
- `nvcc` only for `build-inputs` (the `k*` fixtures)

`gpu_sm_test`:

- Root (`sudo`)
- NVIDIA driver + GPU
- A bpftime build with CUDA attach support (`-DBPFTIME_ENABLE_CUDA_ATTACH=1`)

Missing root, GPU or bpftime → exit `77` (SKIP), not `FAIL`.

---

## Adding a New Probe

```text
gpu_sm.bpf.c    add the kprobe / kretprobe, keep the thread-0 filter
gpu_sm_test.c   add the probe to the hooks[] table
common.h           add an EVENT_SM_* id if the new record needs one,
                   and a field to struct dev_event if the daemon needs it
```

If the new probe emits a different record shape, the Go mirror struct in
`wedjatd` has to change with it — `dev_event` is a wire format and both sides
read it byte for byte.
