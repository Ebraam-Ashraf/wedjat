# Wedjat Daemon — Configuration Reference

The daemon reads its configuration from `/etc/wedjat/config.yaml`. If the file
does not exist, it is created automatically with the defaults listed below. If
a field is absent from the file, the default for that field is used. The daemon
validates all values at startup and refuses to run if any are out of range.

---

## File layout

```yaml
storage:
  day_files_days: 30
  processes_days: 90
  incidents_days: 90

polling:
  nvml_db_tick_ms: 2000
  nvml_socket_tick_ms: 500
  ebpf_drain_tick_ms: 1000
```

There are two top-level sections: `storage` controls what gets kept and for how
long; `polling` controls how often data is sampled and drained.

---

## `storage` — Data Retention

### `day_files_days`

**Default:** `30`  
**Valid range:** `>= 0` (0 = keep forever)

How many UTC daily database files to retain. Each UTC day produces one SQLite
file (`YYYY-MM-DD.db`) holding `gpu_samples` and `agg` rows for that day. The
background day timer deletes files older than this many days at midnight and
on each startup. Set to `0` to never delete daily files.

### `processes_days`

**Default:** `90`  
**Valid range:** `>= 0` (0 = keep forever)

How many days to retain ended process rows in `meta.db`. A process row is
considered ended once the daemon has recorded its exit. Rows older than this
threshold are pruned from `procs` and `proc_gpu` on startup and by the
background day timer. Active (still-running) processes are never pruned
regardless of age.

### `incidents_days`

**Default:** `90`  
**Valid range:** `>= 0` (0 = keep forever)

How many days to retain incident rows (Xid hardware faults and CUDA sync
stalls/hangs) in `meta.db`. Incidents older than this threshold are deleted on
startup and by the background day timer.

---

## `polling` — Sampling Cadences

### `nvml_db_tick_ms`

**Default:** `2000` (2 seconds)  
**Valid range:** `> 0`

How often (in milliseconds) the NVML sampler sends GPU and process data to the
DB layer. This controls the write cadence to `gpu_samples` in the daily
database. Lowering this produces finer-grained time-series data at the cost of
more disk writes and a larger database. The NVML hardware is polled as often as
needed to satisfy both `nvml_db_tick_ms` and `nvml_socket_tick_ms`; this field
only gates the DB output, not the poll frequency itself.

### `nvml_socket_tick_ms`

**Default:** `500` (0.5 seconds)  
**Valid range:** `> 0`

How often (in milliseconds) the NVML sampler sends GPU and process data to
connected socket clients. This field has no effect when no clients are
connected — the sampler drops back to the slower `nvml_db_tick_ms` cadence.
As soon as a client connects, polling accelerates to satisfy this faster
interval so the live TUI or API consumer gets low-latency updates.

### `ebpf_drain_tick_ms`

**Default:** `1000` (1 second)  
**Valid range:** `>= 0` (0 = use tracer default, currently 1000 ms)

How often (in milliseconds) the eBPF session drains the kernel-side aggregate
maps. The kernel accumulates per-CPU counts for CUDA launches, memory copies,
allocations, sync calls, driver ioctls, and UVM faults in BPF hash maps. On
each drain tick the daemon calls `LookupAndDelete` on those maps, merges the
per-CPU values, and sends the result to the DB layer for folding into per-minute
`agg` rows. A lower value produces more frequent, smaller aggregate batches. Set
to `0` to use the compiled-in tracer default.

---

## Effect on the startup sequence

The daemon applies configuration in this order during startup:

1. Load and validate the file; abort if any value is out of range.
2. Open `meta.db` and prune with `processes_days` / `incidents_days`.
3. Start the day timer, which will delete daily files older than `day_files_days`
   at each UTC midnight.
4. Pass `nvml_db_tick_ms` and `nvml_socket_tick_ms` to the NVML sampler.
5. Pass `ebpf_drain_tick_ms` to the eBPF tracer session.

Changing the config file requires a daemon restart:

```bash
sudo systemctl restart wedjatd
```

---

## Tuning guidance

| Goal | Adjustment |
|---|---|
| Reduce disk usage | Increase `day_files_days` threshold, or lower `nvml_db_tick_ms` to write fewer but coarser rows |
| Longer history | Increase `day_files_days`, `processes_days`, `incidents_days` |
| Lower-latency live view | Decrease `nvml_socket_tick_ms` (affects only connected clients) |
| Reduce eBPF overhead on very busy machines | Increase `ebpf_drain_tick_ms` to batch more work per drain |
| Finer eBPF time-series resolution | Decrease `ebpf_drain_tick_ms` toward `500` |
