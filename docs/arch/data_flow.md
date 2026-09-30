# Data Flow: Sources → Store → Consumers

This document defines how telemetry travels through Wedjat, and the contract
between the daemon (writer) and the `wedjat` TUI (reader).

---

## 1. The Pipeline

```
sources                store layer                    consumers
───────                ───────────                    ─────────
NVML  ─────────────┐
eBPF  ─────────────┼──→ [Store] ──→ SQLite (disk) ───→ TUI reads history
proc  ─────────────┘         │                          (mode=ro, daemon can be dead)
                             │
                             └──→ unix socket ────────→ TUI reads live
                                  (mandatory)            (current minute only)
```

Two consumers, one writer. The database is the durable record; the Unix socket
is the live feed. Both are part of the product — neither is a debugging extra.

### Sources

| Source | Mechanism | Latency | Feeds |
| :--- | :--- | :--- | :--- |
| **Layer 4 — NVML** | Polled every 2 s | 2 s | `gpu_samples`, per-process VRAM |
| **Layer 1 — process lifecycle** | eBPF tracepoints | event | `procs`, `proc_devices` |
| **Layer 2 — CUDA API** | uprobes on `libcuda.so` | event | `agg` counters, `kernel_events` |
| **Layer 3 — driver/OS** | kprobes | event | `agg` counters, `incidents` |

### The store layer is a faithful sink

The store **does not filter, sample, or discard**. Every field it is handed is
persisted verbatim inside a single transaction. What to keep is decided
*upstream*, by the collector, which simply never accumulates a field it does not
want. This separation is why the store can be trusted: it holds no opinions about
which data matters.

---

## 2. Consumer 1 — SQLite (durable history)

The TUI opens the databases directly in read-only mode:

```go
db, err := store.OpenReadOnly(ctx, storePath, false)  // immutable MUST be false
```

**Rules for readers:**

- Open with `mode=ro` + `query_only(1)`. The daemon uses WAL journaling, so a
  reader never blocks the writer and never observes a torn transaction.
- **`immutable` must be `false` against a running daemon.** Setting it true tells
  SQLite the file never changes, which disables locking and WAL — producing a
  stale page-cache snapshot with no error. It is correct only for cold archived
  copies taken after the daemon has stopped.
- **History must survive a dead daemon.** This is the defining property of the
  recorder. "What did my job do at 03:00" must be answerable with `wedjatd` not
  running.

### Database split

| File | Contents | Lifetime |
| :--- | :--- | :--- |
| `meta.db` | `gpus`, `procs`, `proc_gpu`, `proc_devices`, `incidents`, `dumps`, `daemon_log`, `daemon_state` | Persistent |
| `YYYY-MM-DD.db` | `gpu_samples`, `agg` (hot time-series) | One UTC day, rotated automatically |

The split keeps identity queries off the hot time-series file, and lets
retention delete a day by removing a single file. A "yesterday" query opens
exactly one daily file.

---

## 3. Consumer 2 — Unix socket (live feed)

**The socket is a required component, not an optional extra.** It carries the one
class of data that exists nowhere else: **the in-progress minute.**

The collector's accumulator lives in daemon memory and is destroyed at each
flush. The newest database row is up to 60 seconds stale and 2-second averaged,
so a live "current GPU state" tile cannot be served from disk. The socket is the
only path to it.

```
/run/wedjat/wedjat.sock
```

- The daemon is the **server**; the TUI is the **client**. Connections are
  read-only in the client direction; the TUI never writes telemetry.
- The socket carries the current minute only. Completed minutes are read from
  SQLite.
- A client that connects, reads, and disconnects affects nothing on disk. A
  client that never connects affects nothing either — the daemon writes
  regardless.

### Why both consumers

| Question | Source |
| :--- | :--- |
| "What did my job do at 03:00 last night?" | SQLite |
| "What is the GPU doing right now?" | Socket |
| "Did that short job ever run?" | SQLite, via `kernel_events` |

---

## 4. Liveness detection

`daemon_state.heartbeat_ts` in `meta.db` is the daemon's liveness marker. The
collector beats every 10 seconds, well inside the 60-second flush interval. A
TUI or CLI must treat a heartbeat older than a small multiple of the beat
interval as "daemon not running" and fall back to SQLite-only reads.

Prefer `store.HeartbeatAge` over reading the raw value: it computes the age
against the same clock the writer used and returns an error on a malformed
timestamp rather than a silently wrong duration.

## 5. Attribution

Per-minute rows reference ledger keys, never raw OS identifiers:

| Column | References | Registered by |
| :--- | :--- | :--- |
| `gpu_samples.gpu_id` | `gpus.gpu_id` | `UpsertGPU` at collector start |
| `agg.gpu_id` | `gpus.gpu_id` | same |
| `agg.proc_id` | `procs.proc_id` | `UpsertProcess` on first sight |

A PID is not an identity. The kernel recycles PIDs, so the same number can
refer to a different program moments after it exits. Identity is therefore
`(boot_id, tgid, start_ticks)`, where `start_ticks` is field 22 of
`/proc/<pid>/stat` — the process start time in clock ticks since boot, which
changes with every execution.

The collector caches the resolved `proc_id` per PID and revalidates it against
`start_ticks` on each poll, so a reused PID is detected and re-resolved rather
than inheriting the previous process's row. A process that exits between
sampling and flushing can no longer be read from `/proc`; its data is folded
into the reserved `<unattributed>` row (id `0`) rather than risk being attached
to whatever process now owns that PID.

Both `gpus.gpu_id = 0` and `procs.proc_id = 0` are reserved sentinels seeded at
store open. Real hardware and real processes always receive ids above `0`.

## 6. Data retention layers

Not every signal deserves the same storage. Three tiers, cheapest first:

| Tier | Table | Content | Cost | Why |
| :--- | :--- | :--- | :--- | :--- |
| 1. Process ledger | `procs` | who ran, command, start/end, exit code | Cheap, always on | Identity. Makes every later row attributable. |
| 2. Minute aggregates | `gpu_samples`, `agg` | board telemetry + per-proc counters | 30 rows/hour | Answers "was the GPU busy, and by whom". |
| 3. Event log | `kernel_events` | name, grid/block, duration | One row per launch | The only tier that survives a sub-second kernel. |

**Tier 2 cannot answer "did a 220 ms job run?"** A 2048×2048 tiled matmul
launches kernels for single-digit milliseconds; a poll-based sampler at any
sane interval will miss them. Tier 3 must be event-driven. This is a property of
the workload, not a tuning problem.

## 7. Implementation status

| Component | State |
| :--- | :--- |
| Stage 1 — store, daily rotation, retention, safety | Implemented |
| Stage 2 — NVML collector, 2s poll / 60s flush | Implemented |
| Stage 3 — socket server, live in-progress minute | Implemented |
| GPU identity registration (`UpsertGPU`) | Implemented |
| Process ledger attribution (`UpsertProcess`, `MapProcessDevice`) | Implemented |
| Liveness heartbeat | Implemented, 10s interval |
| eBPF stages 1–3 | **Not wired** — no probes loaded |
| `kernel_events` table | **Not in schema** |
| `wedjat` CLI | **Not written** |
| Config file parsing | **Not wired** — `config.yaml` is inert |

Because no eBPF stage runs, the `launches`, `memcpy_*`, `alloc_*`, `sync_*`,
`uvm_*`, and `ioctl_calls` columns in `agg` are structurally zero. The schema
and the writer already accept them; nothing produces them yet.
