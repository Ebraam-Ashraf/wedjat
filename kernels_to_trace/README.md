# kernels_to_trace

CUDA workload programs used to test the Wedjat eBPF probes.

## Build & run

```sh
# from wedjat/kernels_to_trace/
make                        # build all k*.cu  →  build/k1, build/k2, ...
make build/k1               # build only k1
make clean                  # wipe build/

./build/k1                  # run k1 on its own (confirm it works before testing)
./build/k2
```

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
