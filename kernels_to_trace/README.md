# kernels_to_trace

CUDA workload programs used to test the Wedjat eBPF probes.

## Build & run

```sh
# from wedjat/kernels_to_trace/
make                        # build all k*.cu  →  build/k1, build/k2, build/k3, ...
make build/k1               # build only k1
make clean                  # wipe build/

./build/k1                  # run on its own (confirm it works before testing)
./build/k2
./build/k3
```

## Kernels

| Binary | File | What it does | Key EVENT_* ids fired |
|--------|------|--------------|----------------------|
| `k1` | `k1.cu` | Element-wise `scale_add` over a 1 M-float device array — 20 launch+sync iterations | `EVENT_ALLOC` `EVENT_MEMCPY` `EVENT_LAUNCH` `EVENT_SYNC` `EVENT_FREE` |
| `k2` | `k2.cu` | Tiled 32×32 shared-memory SGEMM (2048×2048 by default) — 5 iterations, high register pressure | `EVENT_ALLOC` `EVENT_MEMCPY` `EVENT_LAUNCH` `EVENT_SYNC` `EVENT_FREE` |
| `k3` | `k3.cu` | Multi-pass warp-shuffle parallel reduction over a **UVM** buffer (64 MB by default) using async streams — intentionally skips prefetch so GPU demand-faults on first access | `EVENT_ALLOC` (UVM) `EVENT_MEMCPY` `EVENT_LAUNCH` `EVENT_SYNC` `EVENT_FREE` `EVENT_UVM_FAULT` |

## Add a workload

1. Create `kN.cu` in this directory — no arguments, no stdin, exits 0, finishes in under 10 seconds
2. `make` — picks it up automatically, no Makefile changes needed
3. `./build/kN` — confirm it runs cleanly on its own

The uprobe tests in `daemon/ebpf/uprobes/test/` scan `build/` for `k*` binaries
automatically. Nothing to change there either.

## Rules for a good workload

- No command-line arguments, no stdin
- Always exits 0 on success
- Does the same thing every run (deterministic)
- Finishes in under 10 seconds
