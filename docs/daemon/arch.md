# Wedjat Daemon — Architecture

This document covers the runtime architecture of `wedjatd`: how telemetry
travels from hardware to persistent storage, how internal components are
structured, and what happens at startup and shutdown.

---

## 1. Full System Diagram

```
┌────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                                 WEDJAT DAEMON (wedjatd)                                                │
│                                                                                                                        │
│ ┌────────────────────────────────────────────────── STARTUP ─────────────────────────────────────────────────────────┐ │
│ │ acquire lock ─► load/validate config ─► open SQLite ─► prune metadata ─► boot ID ─► clean_shutdown=false         │ │
│ │ ─► day timer (UTC) ─► NVML init/discover ─► NewChans(x2) ─► Start Consumers(DB/Sock) ─► Start Sources(NVML/eBPF) │ │
│ └────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                                                        │
│     ┌────────────────────────────────────────────────┐         ┌────────────────────────────────────────────────┐      │
│     │               NVIDIA / NVML SOURCE             │         │                KERNEL / eBPF SOURCE            │      │
│     │  (C poller.c / xid.c ◄── cgo ──► Go bridge)    │         │               (BPF programs in kernel)         │      │
│     │ ┌─────────────────────┐  ┌───────────────────┐ │         │ ┌─────────────────────┐  ┌───────────────────┐ │      │
│     │ │      POLL LOOP      │  │     XID LOOP      │ │         │ │        LOOP A       │  │      LOOP B       │ │      │
│     │ │ (0.5s active /      │  │ (1s wait timeout, │ │         │ │     (Continuous)    │  │   (1s interval)   │ │      │
│     │ │  2s idle polling)   │  │  event-driven)    │ │         │ │ - Read events_pipe  │  │ - Drain agg_map   │ │      │
│     │ │ - PollGPU           │  │ - Await Xid       │ │         │ │ - Decode Event      │  │ - Scan cuda_sync  │ │      │
│     │ │ - PollProcesses     │  │                   │ │         │ │ - Clean proc states │  │ - Stats / drops   │ │      │
│     │ └──────────┬──────────┘  └─────────┬─────────┘ │         │ └──────────┬──────────┘  └─────────┬─────────┘ │      │
│     └────────────┼───────────────────────┼───────────┘         └────────────┼───────────────────────┼───────────┘      │
│                  ▼                       ▼                                  ▼                       ▼                  │
│       [[]GPUSample, ProcList]          [Xid]                             [Event]                [[]AggRow]             │
│                  │                       │                                  │                       │                  │
│                  └───────────────────────┴─────────────────┬────────────────┴───────────────────────┘                  │
│                                                            │                                                           │
│ ┌──────────────────────────────────────────────────────────▼─────────────────────────────────────────────────────────┐ │
│ │                                               CHANNEL ROUTING LAYER                                                │ │
│ │                               (Non-blocking Sends; full = drop + source.Dropped++)                                 │ │
│ │                                                                                                                    │ │
│ │       ┌──────────────────────── DB SET ──────────────────────┐ ┌─────────────────── SOCKET SET ──────────────────┐ │ │
│ │       │ (Buffer: 4096, Always Active)                        │ │ (Buffer: 64, Only sent if source.Clients > 0)   │ │ │
│ │       │ ┌───────┐ ┌───────┐ ┌───────┐ ┌───────┐ ┌──────────┐ │ │ ┌───────┐ ┌───────┐ ┌───────┐ ┌───────┐ ┌─────┐ │ │
│ │       │ │  GPU  │ │ Procs │ │  Xid  │ │  Agg  │ │  Event   │ │ │ │  GPU  │ │ Procs │ │  Xid  │ │  Agg  │ │Event│ │ │
│ │       │ └───┬───┘ └───┬───┘ └───┬───┘ └───┬───┘ └────┬─────┘ │ │ └───┬───┘ └───┬───┘ └───┬───┘ └───┬───┘ └──┬──┘ │ │
│ │       └─────┼─────────┼─────────┼─────────┼──────────┼───────┘ └─────┼─────────┼─────────┼─────────┼────────┼──┘ │ │
│ └─────────────┼─────────┼─────────┼─────────┼──────────┼───────────────┼─────────┼─────────┼─────────┼────────┼────┘ │
│               │         │         │         │          │                 │         │         │         │        │      │
│               ▼         ▼         ▼         ▼          ▼                 ▼         ▼         ▼         ▼        ▼      │
│ ┌──────────────────────────────────────────────────────────────┐ ┌───────────────────────────────────────────────────┐ │
│ │                          DB LAYER                            │ │                   SOCKET LAYER                    │ │
│ │  (1 Goroutine, 1s Flush Tick, 10s Heartbeat, context aware)  │ │              (Unix Socket, Mode 0660)             │ │
│ │                                                              │ │                                                   │ │
│ │  [ MEMORY FOLDING ]                                          │ │  [ BROADCASTER ]                                  │ │
│ │  ├─ GPU: 1-min samples (unset valid bit = NULL)              │ │  ├─ Selects over the 5 Socket Channels            │ │
│ │  ├─ Procs: Read /proc identity, track VRAM + seen flag       │ │  └─ Encodes ONCE ─► JSON:                         │ │
│ │  ├─ Agg: Resolve (tgid)->/proc environ->GPU, fold per API    │ │     {"type", "timestamp_unix_nano", "data"}\n     │ │
│ │  ├─ Event: Register EXEC, queue EXIT. Check SYNC / Hung      │ │               │                                   │ │
│ │  └─ Xid: Track incident xid                                  │ │               ▼                                   │ │
│ │                                                              │ │  [ CLIENT GOROUTINES ]                            │ │
│ │  [ 1s FLUSH BATCH ]                                          │ │  ├─ Accept loop ─► Clients++ (defer Clients--)    │ │
│ │  ├─ Sweep Procs (only if latest ProcList is Complete)        │ │  ├─ Each client has own buffer (Size: 8)          │ │
│ │  ├─ 1 Tx per DB. If flush fails: keep batch, retry, cap Q    │ │  ├─ Drop messages if client buffer is full        │ │
│ │  └─ Heartbeat(10s): log source drops + unattributed growth   │ │  └─ Write loop (5s deadline) ─► Drop on error     │ │
│ └─┬──────────────────────────────────────────────────────────┬─┘ └───────────────────────────────────────────────────┘ │
│   │                                                          │                                                         │
│   ▼                                                          ▼                                                         │
│ ┌───────────────────────────┐    ┌──────────────────────────────────────────────┐                                      │
│ │          meta.db          │    │         YYYY-MM-DD.db (UTC Daily)            │                                      │
│ │ ├─ gpus, daemon_state     │    │ ├─ gpu_samples (folded to minute resolution) │                                      │
│ │ ├─ procs, proc_gpu, VRAM  │    │ ├─ agg (folded eBPF metrics per API)         │                                      │
│ │ └─ incidents              │    │ └─ Daily file rotated at UTC midnight        │                                      │
│ └───────────────────────────┘    └──────────────────────────────────────────────┘                                      │
│                                                                                                                        │
│ ┌───────────────────────────────────────────────── SHUTDOWN ─────────────────────────────────────────────────────────┐ │
│ │  eBPF Close ─► Cancel NVML+Xid ─► Socket Stop ─► DB Layer Stop (final flush, 5s context) ─► clean_shutdown=true  │ │
│ │                                (only if flush OK) ─► Close DB ─► Release Lock                                      │ │
│ └────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Sources

There are two independent telemetry sources. Both run concurrently and feed into
the same two channel sets.

### NVIDIA / NVML Source

Implemented as a C polling library (`poller.c`, `xid.c`) called from Go via cgo.
It has two loops:

**Poll loop** — runs on a timer driven by `nvml_db_tick_ms` (idle) and
`nvml_socket_tick_ms` (active, when clients are connected). Each tick calls
`PollGPU` and `PollProcesses` and emits a `[]GPUSample` and a `ProcList`.

**Xid loop** — blocks waiting for hardware fault events with a 1-second timeout.
Each Xid event is emitted immediately as a `Xid` message.

NVML is best-effort. If the NVIDIA driver is not installed or `nvmlInit` fails,
the daemon logs a warning and continues with eBPF telemetry only.

### Kernel / eBPF Source

Four BPF objects are loaded and attached at startup:

| Object | Responsibility |
|---|---|
| `cuda_actions` | CUDA alloc, free, copy, launch, and sync calls (uprobes on `libcuda.so`) |
| `host_ctx` | CUDA context and device-ordinal bindings |
| `driver_kprobes` | NVIDIA driver and UVM ioctl / fault / migration events |
| `proc_lifecycle` | Process exec and final-thread exit events |

The eBPF source also has two loops:

**Loop A (continuous)** — reads the `events_pipe` ring buffer. Decodes each
event, cleans up per-process BPF state on exit, and forwards events to the DB
and socket channels.

**Loop B (1s interval, configured by `ebpf_drain_tick_ms`)** — calls
`LookupAndDelete` on the aggregate maps, merges per-CPU counters, and emits
`[]AggRow` messages.

eBPF loading is best-effort. A partial probe set is logged; if no objects can
load, NVML continues alone.

---

## 3. Channel Routing Layer

Sources do not write to the database or socket directly. They send typed
messages through two sets of bounded Go channels:

| Set | Buffer | When active |
|---|---|---|
| DB Set | 4096 per channel | Always |
| Socket Set | 64 per channel | Only when `source.Clients > 0` |

There are five channel types in each set: `GPU`, `Procs`, `Xid`, `Agg`, `Event`.

All sends are **non-blocking**. If a channel is full the message is dropped and
`source.Dropped` is incremented. The DB layer logs drop counts every 10 seconds
via its heartbeat.

---

## 4. DB Layer

A single goroutine reads the DB channel set. It accumulates a batch and flushes
approximately every 1 second.

**Memory folding:**

- **GPU samples** — keyed by UTC minute × GPU UUID. Each field is folded as a
  sum, max, or bitwise OR. Fields with no valid reading become SQL `NULL`.
- **Processes** — identified by `(boot_id, tgid, start_ticks)` from
  `/proc/<pid>/stat`. PID reuse is detected by revalidating `start_ticks` on
  each reference. VRAM and `last_seen` are updated from NVML process
  observations. Process exit is driven by complete NVML sweeps and eBPF exit
  events.
- **Aggregates** — the `tgid` + CUDA device ordinal is resolved to a physical
  GPU UUID by reading `CUDA_VISIBLE_DEVICES` from `/proc/<pid>/environ`. Rows
  are folded per API into the per-process/per-GPU minute `agg` table.
- **Events** — exec events register new processes; exit events queue a close.
  Slow and hung sync events create incidents.
- **Xid** — each Xid report creates or updates an incident row in `meta.db`.

**Flush behavior:** one transaction per database file per flush. A failed flush
retains the batch for retry; retained work is capped at 100,000 rows. On
shutdown the layer drains queued messages and then tries a final flush with a
5-second context deadline. The daemon marks the run clean only if that flush
succeeds.

**Heartbeat:** every 10 seconds the DB layer logs drop counts and any growth in
unattributed aggregates.

---

## 5. Socket Layer

`core/socket.go` reads the socket channel set. It encodes each message once as
a newline-terminated JSON envelope and broadcasts it to every connected client:

```json
{"type":"agg","timestamp_unix_nano":1790000000000000000,"data":[]}
```

`type` values: `gpu`, `procs`, `xid`, `agg`, `event`.

Each client gets its own goroutine and an 8-message output buffer. A slow or
disconnected client has its messages dropped; it does not block other clients or
any producer. Each write has a 5-second deadline.

Connecting a client increments `source.Clients`, which causes:
1. eBPF and NVML sources to begin copying messages into the Socket Set.
2. The NVML poll loop to accelerate to `nvml_socket_tick_ms`.

---

## 6. Storage Layout

| File | Tables | Lifetime |
|---|---|---|
| `meta.db` | `gpus`, `procs`, `proc_gpu`, `incidents`, `daemon_state` | Persistent |
| `YYYY-MM-DD.db` | `gpu_samples`, `agg` | One UTC day, rotated at midnight |

The split keeps identity queries off the hot time-series file and lets the
retention policy delete a full day by removing a single file.

`daemon_state` holds the current `boot_id`, `clean_shutdown` flag, and
`heartbeat_ts`. A stale heartbeat (older than ~30 seconds) means the daemon is
not running.

---

## 7. Startup Sequence

1. Acquire the exclusive process lock (`/run/wedjat/daemon.lock`).
2. Load and validate `/etc/wedjat/config.yaml`; write defaults if absent.
3. Open `meta.db` and today's daily database.
4. Prune `procs` and `incidents` according to retention config.
5. Read the system boot ID from `/proc/sys/kernel/random/boot_id`.
6. Check `daemon_state` for a previous unclean shutdown; log a warning if found.
7. Close processes left open by earlier boots; write the current boot ID.
8. Set `clean_shutdown = false`.
9. Start the UTC day timer (daily file rotation + metadata prune).
10. Initialize NVML; discover and register GPUs in `meta.db`. Best effort.
11. Allocate two `source.Chans` sets: DB (buffer 4096) and Socket (buffer 64).
12. Start the DB layer goroutine.
13. Start the Unix socket server.
14. Start the NVML polling goroutine (only if NVML init succeeded).
15. Start the eBPF tracer session. Best effort.
16. Block on `SIGINT` / `SIGTERM`.

---

## 8. Shutdown Sequence

On `SIGINT` or `SIGTERM`:

1. Close the eBPF session (detach probes, drain remaining ring buffer).
2. Cancel the NVML poll loop and Xid loop; wait for them to exit.
3. Call `nvmlShutdown`.
4. Stop the Unix socket server; remove the socket file.
5. Stop the DB layer: drain queued channel messages, final flush with a 5-second
   context. If the flush fails, shutdown is marked unclean.
6. Stop the day timer.
7. Write `clean_shutdown = true` (only on successful final flush).
8. Close SQLite.
9. Release the process lock.

---

## 8. Package Map

| Package | Responsibility |
|---|---|
| `daemon/internal/daemon` | Startup, shutdown, signal handling, lock management |
| `daemon/core` | Config, Unix socket server |
| `daemon/core/db` | SQLite open/close, identity, folding, flush, retention, day timer |
| `daemon/core/source` | Typed messages, channel sets, drop counter |
| `daemon/core/source/nvml` | cgo bridge, poll loop, Xid loop |
| `daemon/core/source/ebpf` | BPF object load/attach, ring buffer loop, aggregate drain |
| `daemon/nvml` | C NVML polling library (poller.c, xid.c) |
| `daemon/ebpf` | BPF C programs (cuda_actions, host_ctx, driver_kprobes, proc_lifecycle) |

---

## 9. The Five Telemetry Layers

| Layer | What it captures | How |
|---|---|---|
| 1 | Host process identity: pid, tid, comm, cgroup_id, CPU core, timestamp | eBPF built-ins at every probe site |
| 2 | CUDA API intent: launches, allocs, copies, syncs | uprobes / uretprobes on `libcuda.so` |
| 3 | OS and driver activity: ioctls, UVM faults, scheduler events | kprobes on `nvidia.ko`, `nvidia-uvm.ko` |
| 4 | Physical hardware state: power, temperature, clocks, VRAM, processes | NVML polling |
| 5 | On-chip micro-telemetry: SM placement, warp divergence, memory coalescing | PTX injection via bpftime (not loaded by wedjatd) |

Layer 5 is exercised separately through the bpftime GPU test setup and is not
part of the daemon's normal BPF object load.
