# uprobes/test

Tests for `cuda_actions.bpf.c` and `host_ctx.bpf.c`.

## Build & run

```sh
# from wedjat/daemon/ebpf/uprobes/test/
make                                             # build kernels + BPF objects + skeletons + test binaries → build/
make run                                         # build everything then run both tests (needs sudo + GPU)
make run DUMP=1                                  # same but also print raw agg_map after each kernel
make run LIBCUDA=/usr/lib/x86_64-linux-gnu/libcuda.so.1   # override libcuda path if auto-detect fails
make clean                                       # wipe build/

sudo build/cuda_actions_test build/              # run only cuda_actions_test
sudo build/host_ctx_test     build/              # run only host_ctx_test

sudo DUMP=1   build/cuda_actions_test build/     # cuda_actions_test with agg_map dump
sudo LIBCUDA=/path/to/libcuda.so.1 build/cuda_actions_test build/   # with explicit libcuda

# find libcuda path if needed
ldconfig -p | grep libcuda
```

## What each test does

**`cuda_actions_test`** — loads `cuda_actions.bpf.o`, attaches all uprobes to
`libcuda.so` once, then runs every `k*` binary found in `build/` and checks
`agg_map` for that binary's pid. Prints one row per kernel showing call counts
per api_id, then a coverage summary.

**`host_ctx_test`** — loads `host_ctx.bpf.o`, attaches context uprobes once,
runs every `k*` binary, and checks that `tid_to_device` has an entry for the
binary's main thread after it exits.

## Needs

- Root (`sudo`)
- NVIDIA driver + GPU (`/dev/nvidiactl`)
- `nvcc`, `clang`, `bpftool`, `libbpf-dev`

Missing GPU or root → exit 77 (SKIP), not FAIL.
