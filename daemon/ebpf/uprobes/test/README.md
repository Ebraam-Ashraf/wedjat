# uprobes/test

Tests for `cuda_actions.bpf.c` and `host_ctx.bpf.c`.

---

## Build & Run

```sh
cd daemon/ebpf/uprobes/test

make                          # build everything → build/
make test                     # build + run both tests (needs sudo + GPU)
make test TARGET=cuda_actions  # run only cuda_actions_test
make test TARGET=host_ctx     # run only host_ctx_test
make clean                    # wipe build/

# useful variables
make test DUMP=1              # also print every raw event after each kernel
make test LIBCUDA=/path/to/libcuda.so.1   # override libcuda path if needed
```

---

## What Each Test Does

### `cuda_actions_test`

Loads `cuda_actions.bpf.o` and `host_ctx.bpf.o` together — the context
hooks (`host_ctx`) must run alongside the action hooks so that `tid_to_device`
and `ctx_to_device` get populated. Without them every `cuda_actions` probe
returns early (no device mapping) and no events reach the ring buffer.

Both objects share the same `tid_to_device` and `ctx_to_device` maps via
`bpf_map__reuse_fd()`.

For every `k*` binary found in `build/`:

1. Fork + exec the binary.
2. Poll `events_pipe` ring buffer while it runs, filtering to that child's pid.
3. Tally events by `api_id` from `enum event_id` in `common.h`.
4. Print one row per binary showing call counts per event type.
5. After all binaries, print a coverage summary — warn about any `EVENT_*`
   id that no binary triggered.

```text
kernel      LAUNCH     ALLOC      FREE     MEMCPY      SYNC   result
k1              20         1         1          2        20   ok
k2               5         3         3          3         5   ok
k3              24         4         4         16        32   ok
```

Tracked events: `EVENT_LAUNCH`, `EVENT_ALLOC`, `EVENT_FREE`, `EVENT_MEMCPY`,
`EVENT_SYNC`.

### `host_ctx_test`

Loads `host_ctx.bpf.o` and attaches all context lifecycle uprobes once.
For every `k*` binary:

1. Fork + exec the binary.
2. After it exits, look up the binary's pid in `tid_to_device`.
3. The entry must exist — confirms that `cuCtxCreate`/`cuCtxSetCurrent`
   fired and the TID→device mapping was written correctly.

```text
kernel      tid_found  device  result
k1          yes        0       ok
k2          yes        0       ok
k3          yes        0       ok
```

---

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `LIBCUDA` | `libcuda.so.1` | Path to `libcuda.so` — set if auto-detect fails |
| `DUMP` | unset | Print every raw `struct event` while running |

---

## Requirements

- Root (`sudo`)
- NVIDIA driver + GPU (`/dev/nvidiactl`)
- `nvcc`, `clang`, `bpftool`, `libbpf-dev`

Missing root or GPU → exit `77` (SKIP), not `FAIL`.

---

## Adding a New Probe

```text
cuda_actions.bpf.c    add the uprobe / uretprobe
cuda_actions_test.c   add the EVENT_* id to TRACKED_IDS[] and TRACKED_NAMES[]

host_ctx.bpf.c        add the uprobe / uretprobe
host_ctx_test.c       add the symbol to hooks[]
```
