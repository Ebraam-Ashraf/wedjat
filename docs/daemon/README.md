# Daemon documentation

Start with the current runtime description:

- [Architecture](arch.md) — components, startup/shutdown sequence, channel
  routing, storage layout, and package map.
- [Telemetry flow](data_flow.md) — how source messages travel through channels
  to the database and socket clients.
- [Configuration](config.md) — all config file fields, defaults, valid ranges,
  and tuning guidance.

The layer guides provide deeper background on each telemetry layer:

- [Layer 1](layer_1.md) — host process and thread identity from eBPF built-ins
- [Layer 2](layer_2.md) — CUDA API interception via uprobes on `libcuda.so`
- [Layer 3](layer_3.md) — OS and NVIDIA driver tracing via kprobes
- [Layer 4](layer_4.md) — physical hardware telemetry via NVML
- [Layer 5](layer_5.md) — on-chip micro-telemetry via PTX injection (bpftime)

Note: the layer guides describe the full intended scope of each layer. Not
every probe or capability described in them is active in the current daemon.
Layer 5 in particular is not loaded by `wedjatd` — it is exercised separately
through the bpftime GPU test setup. Check each guide's runtime-status note
before treating its examples as implementation details.
