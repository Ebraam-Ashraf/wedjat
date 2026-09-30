# NVML collector

The NVML layer provides device telemetry, process VRAM snapshots, and Xid event
plumbing. Snapshots are keyed by stable GPU UUID; indices are returned as
metadata and are not used as persistent device identity. Unsupported fields
remain zero-initialized in C but are unavailable unless their bit is set in
`valid_fields`. Preserve the exact `nvml_error` value for diagnostics.

Device metadata is intended to be read once during startup and includes UUID,
name, PCI bus ID, and driver version. Device samples include utilization,
memory, temperature, power, clocks, throttle/event reasons, power limit, and
volatile/aggregate ECC counters. Per-field validity flags distinguish missing
metrics from real zero readings.

Process snapshots combine compute, graphics, and MPS process lists. The list
size is queried dynamically and retried if it grows. `NVML_VALUE_NOT_AVAILABLE`
VRAM is represented with `memory_valid == 0`, never as a huge byte count. The
caller owns the returned entries and must call
`poller_process_snapshot_destroy`.

Xid monitoring uses one NVML event set registered against every enumerated GPU.
For each event, the returned NVML device handle supplies both the UUID and
current NVML index; the event is timestamped when wait returns. Manual mode
waits in one-second intervals until a real Xid event arrives.

The C APIs are synchronous. A Go collector that uses them should isolate
per-device polls and enforce deadlines; a blocked NVML call can otherwise delay
that caller. The daemon's Go integration should dynamically load NVML if it must
start on hosts without NVIDIA driver libraries. PID attribution from `/dev/kmsg`
is not implemented here.

## Build and test

```sh
make -C daemon/nvml/test build
make -C daemon/nvml/test test
make -C daemon/nvml/test run-xid-test-manual
```

The automated poller test loops over all visible GPUs, checks metadata and
available sample ranges, then launches a CUDA fixture with each GPU UUID in
`CUDA_VISIBLE_DEVICES` and verifies process attribution and VRAM when NVML
reports it. Multi-GPU coverage is naturally skipped on single-GPU hosts. Xid
automated coverage verifies registration and a clean timeout; it does not
induce a hardware fault. Exit code 77 is treated as a skip by both Make targets.

Hardware tests require a working NVIDIA driver, `libnvidia-ml.so`, CUDA headers,
and the CUDA fixture compiler. The manual Xid test is intentionally not part of
`make test`.
