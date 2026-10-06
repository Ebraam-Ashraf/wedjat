# Wedjat Daemon — Telemetry Flow

This page traces every message type from its source through channels to the
database or a socket client. Package paths are `daemon/core/source`,
`daemon/core/db`, and `daemon/core/socket.go`.

---

## At a glance

```
                    ┌─────────────────────────────────┐
                    │         NVML SOURCE              │
                    │  Poll loop  │  Xid loop          │
                    │  GPUSample  │  Xid               │
                    │  ProcList   │  (event-driven)     │
                    └──────────────┬──────────────────-┘
                                   │
                    ┌──────────────┘
                    │
                    │              ┌──────────────────────────────┐
                    │              │         eBPF SOURCE          │
                    │              │  Loop A (continuous)         │
                    │              │  - Ring buffer → Event       │
                    │              │  Loop B (1s drain)           │
                    │              │  - Aggregate maps → AggRow   │
                    │              └──────────────┬───────────────┘
                    │                             │
                    └──────────────┬──────────────┘
                                   │ source.Send (non-blocking)
                                   ▼
               ┌───────────────────────────────────────────────┐
               │              CHANNEL ROUTING                  │
               │                                               │
               │  DB SET (cap 4096)    SOCKET SET (cap 64)     │
               │  GPU  Procs  Xid      GPU  Procs  Xid         │
               │  Agg  Event           Agg  Event              │
               │  always active        only if Clients > 0     │
               └──────────────┬──────────────────┬────────────-┘
                              │                  │
                              ▼                  ▼
               ┌──────────────────┐   ┌──────────────────────┐
               │    DB LAYER      │   │    SOCKET LAYER       │
               │  1s flush batch  │   │  JSON broadcast       │
               │  10s heartbeat   │   │  per-client buffer=8  │
               └──────┬─────┬────-┘   └──────────────────────-┘
                      │     │
                      ▼     ▼
              meta.db   YYYY-MM-DD.db
```

---

## Message types

| Type | Produced by | DB destination | Socket `"type"` |
|---|---|---|---|
| `[]GPUSample` | NVML poll loop | `gpu_samples` (daily, folded to minute) | `"gpu"` |
| `ProcList` | NVML poll loop | `procs`, `proc_gpu` in `meta.db` | `"procs"` |
| `Xid` | NVML Xid loop | `incidents` in `meta.db` | `"xid"` |
| `[]AggRow` | eBPF drain (Loop B) | `agg` (daily, folded to minute) | `"agg"` |
| `Event` | eBPF ring buffer (Loop A) | process identity + incidents | `"event"` |

---

## NVML source

`daemon/core/source/nvml/run.go` bridges to the C poller via cgo.

**Poll loop cadence:**
- No clients connected → polls at `nvml_db_tick_ms` (default 2000 ms). DB
  channels receive data on this interval.
- Clients connected → polls at `nvml_socket_tick_ms` (default 500 ms). DB
  channels still receive data only at `nvml_db_tick_ms`; the dispatcher gates
  them independently.

Each poll produces:
- One `[]GPUSample` (one entry per discovered GPU UUID).
- One `ProcList` with a `Complete` flag. An incomplete list — caused by a
  failed GPU or process query — is sent to the DB layer but will not trigger
  process sweeps. A complete empty list is meaningful: it closes all processes
  observed this boot.

**Xid loop:** blocks on a hardware event wait (1s timeout). Each event is sent
to DB channels immediately. It is also sent to socket channels if clients are
connected.

---

## eBPF source

`daemon/core/source/ebpf/` loads four BPF objects:

| Object | Probes |
|---|---|
| `cuda_actions` | uprobes on `libcuda.so`: launch, alloc, free, copy, sync |
| `host_ctx` | uprobes on `libcuda.so`: context set, device ordinal binding |
| `driver_kprobes` | kprobes on `nvidia.ko` / `nvidia-uvm.ko`: ioctl, faults, migrations |
| `proc_lifecycle` | tracepoints: exec and final-thread exit |

**Loop A — ring buffer (continuous):** reads `events_pipe`. On each event:
1. Decodes the event struct into a typed `Event`.
2. If it is a process exit, clears BPF map entries for that process generation.
3. Sends the `Event` to DB channels always, and to socket channels if clients
   are connected.

**Loop B — aggregate drain (`ebpf_drain_tick_ms`, default 1000 ms):**
1. Calls `LookupAndDelete` on all aggregate maps.
2. Merges per-CPU counter values.
3. Stamps each row with the drain timestamp.
4. Sends `[]AggRow` to DB channels always, and to socket channels if clients
   are connected.

---

## Channel routing

`source.NewChans(n)` allocates a set of five typed channels each with buffer `n`.
Two sets are allocated at startup:

| Set | Buffer | Send condition |
|---|---|---|
| DB Set | 4096 | Always |
| Socket Set | 64 | Only when `source.Clients > 0` |

All sends are non-blocking (`select` with a `default` branch). A full channel
drops the message and increments `source.Dropped`. The DB layer reports drop
counts in its 10-second heartbeat log line.

---

## DB layer

`daemon/core/db/layer.go` — one goroutine, one batch accumulated per second.

### GPU samples

Each `[]GPUSample` is keyed by UTC minute × GPU DB ID (resolved from UUID).
Fields fold as:
- Utilization, power, temperature → **average** (sum ÷ count).
- Memory used, memory free → **last** value.
- Clock speeds → **maximum**.
- PCIe TX/RX → **sum**.
- Fields with no valid reading → SQL `NULL` (validity bit unset).

### Processes

Process identity is `(boot_id, tgid, start_ticks)`, where `start_ticks` is
field 22 of `/proc/<pid>/stat` (clock ticks since boot). The PID cache
revalidates `start_ticks` on each lookup so a recycled PID starts a new row.

NVML process observations update `proc_gpu` with `first_seen`, `last_seen`,
current VRAM, and peak VRAM.

A **complete** `ProcList` drives a sweep: any process not present in the list
and not already closed is marked as exited. An incomplete list skips the sweep.

eBPF exec events call `UpsertProcess` so short-lived processes are registered
even if NVML never sees them. eBPF exit events record exit code or signal.

### Aggregates

Each `AggRow` contains a `tgid` and a CUDA-visible device ordinal. Attribution:
1. Read `CUDA_VISIBLE_DEVICES` from `/proc/<tgid>/environ`.
2. Map the ordinal to a discovered GPU UUID.
3. Revalidate process start identity while resolving.
4. Fold the API counters (launch count, memcpy calls/bytes, alloc calls/bytes,
   freed bytes, sync count/total/max time, ioctl calls, UVM faults/evictions,
   errors) into the matching `agg` columns for this process × GPU × UTC minute.

If the process or GPU mapping cannot be safely resolved, the row is counted as
unattributed and logged in the heartbeat.

### Incidents

- **Xid events** → upserted into `incidents` in `meta.db`. GPU association uses
  the UUID from the Xid report.
- **Slow sync** (sync duration ≥ `sync_stall_us`) → incident row of type stall.
- **Hung sync** (sync never returned within a threshold) → incident row of type
  hang.

### Flush and retry

Each flush runs one SQLite transaction per database file. `meta.db` and the
daily file are committed separately. A failed flush retains the batch and
retries on the next tick. Retained work is capped at 100,000 rows to bound
memory use.

On shutdown the layer drains all remaining channel messages, then flushes with
a 5-second context. The daemon marks the run clean only if this final flush
succeeds.

---

## Socket layer

`daemon/core/socket.go` — Unix domain socket, mode `0660`.

**Broadcaster goroutine:** selects over the five socket channels. Encodes each
message once:

```json
{"type":"gpu","timestamp_unix_nano":1790000000000000000,"data":[...]}
```

then writes the encoded bytes to every client's output buffer.

**Per-client goroutine:** each accepted connection gets:
- A dedicated goroutine for writes.
- An 8-message output buffer (channel).
- A 5-second per-write deadline.

If the client's buffer is full the message is dropped for that client. A slow
or errored client does not affect other clients or any producer.

**Client count:** `source.Clients` is incremented on accept and decremented on
disconnect. This counter gates socket-channel sends in both sources and triggers
the faster NVML poll cadence.

---

## SQLite files

`daemon/core/db/sql/meta.sql` and `daily.sql` define the schemas.

| File | Table | Purpose |
|---|---|---|
| `meta.db` | `gpus` | Stable GPU identity (UUID, name, index) |
| `meta.db` | `procs` | Process identity and lifecycle |
| `meta.db` | `proc_gpu` | Per-process VRAM and first/last seen per GPU |
| `meta.db` | `incidents` | Xid faults, sync stalls, sync hangs |
| `meta.db` | `daemon_state` | Boot ID, clean_shutdown flag, heartbeat timestamp |
| `YYYY-MM-DD.db` | `gpu_samples` | Minute-resolution board telemetry |
| `YYYY-MM-DD.db` | `agg` | Minute-resolution per-process/per-GPU CUDA counters |

Daily files are rotated at UTC midnight. The background day timer deletes files
older than `storage.day_files_days` days and re-runs the metadata retention
prune.

---

## Configuration defaults

| Setting | Default | Effect |
|---|---:|---|
| `storage.day_files_days` | 30 | Daily SQLite files retained |
| `storage.processes_days` | 90 | Ended process row retention |
| `storage.incidents_days` | 90 | Incident row retention |
| `polling.nvml_db_tick_ms` | 2,000 | NVML → DB cadence |
| `polling.nvml_socket_tick_ms` | 500 | NVML → socket cadence (clients connected) |
| `polling.ebpf_drain_tick_ms` | 1,000 | eBPF aggregate map drain interval |

See [config.md](config.md) for the full configuration reference and
[arch.md](arch.md) for startup/shutdown order and component overview.
