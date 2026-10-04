# Wedjat: data flow from source to DB and socket

This document summarizes the current source, database, and socket flow implemented in the daemon.

---

## 1. The idea

Wedjat is a GPU observability daemon for Linux + NVIDIA. It answers two different questions and then joins the answers:

| Question | Source | What it can tell you |
|---|---|---|
| **What is the GPU doing right now?** | **NVML** (the NVIDIA management library) | Utilization, VRAM, temperature, power, clocks, throttling, ECC errors, which PIDs hold VRAM, Xid hardware errors |
| **What are processes asking the GPU to do?** | **eBPF** (uprobes on `libcuda`, kprobes on the NVIDIA driver, scheduler tracepoints) | CUDA launches, allocations, memcpys, syncs and how long they took, UVM faults/evictions, ioctls, process start/exit |

NVML is a poll: once per tick you ask "what is the state?". eBPF is event-driven: the kernel counts things as they happen and the daemon drains the counters.

Both feed **one identity model**, so the two paths agree on "which GPU" and "which process":

- **GPU identity = UUID** (never the NVML index, which can change).
- **Process identity = `(boot_id, tgid, start_ticks)`**, never a bare PID, because the kernel recycles PIDs.

Both producers feed the source dispatcher. **SQLite** receives NVML and eBPF telemetry; the **unix socket** carries both types while clients are connected.

---

## 2. The whole pipeline

```text
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
│ │       │ (Buffer: 4096, Always Active)                        │ │ (Buffer: 64, Only sent if source.Clients > 0)   │ │
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
│ │  eBPF Close ─► Cancel NVML+Xid ─► Socket Stop ─► DB Layer Stop (final flush, 5s context) ─► clean_shutdown=true    │ │
│ │                                (only if flush OK) ─► Close DB ─► Release Lock                                      │ │
│ └────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘

```

The source dispatcher sends eBPF aggregates and incidents to `DBChannel`
regardless of connected clients, and sends NVML samples to DB at
`nvml_db_tick_ms`. It sends NVML samples at `nvml_socket_tick_ms`, plus eBPF
aggregates and incidents as they arrive, to `SocketChannel` only while one or
more clients are connected. Both output channels use bounded, non-blocking
sends, so a full queue can drop messages instead of blocking a producer.

With no clients, the sampler polls at `nvml_db_tick_ms`. With clients, it polls
at the faster of `nvml_db_tick_ms` and `nvml_socket_tick_ms`; the dispatcher
sends each output at its own configured cadence. After the last client
disconnects, polling returns to the DB interval.

---

## 3. Source 1: NVML (C and Go sampler)

The Go bindings and adaptive sampler live in `daemon/core/source/nvml`; its C
bridge wraps the NVML polling implementation under `daemon/nvml`.

### 3.1 Loading and safety

- `nvml_loader.h` opens `libnvidia-ml.so.1` (then `libnvidia-ml.so`) with `dlopen`. `WEDJAT_NVML_LIBRARY` overrides the path. Every NVML function is resolved lazily with `dlsym` through the `NVML_CALL(name, ...)` macro. If the library or symbol is missing, the call returns `NVML_ERROR_UNKNOWN` instead of crashing.
- All NVML calls are serialized by one mutex (`poller_nvml_lock/unlock`), because recovery may call `nvmlShutdown` + `nvmlInit` while other callers hold device handles.
- If a query returns `UNINITIALIZED` or `DRIVER_NOT_LOADED`, the poller shuts NVML down, re-inits, re-fetches the handle **by UUID**, and retries that query once.

### 3.2 Structs (what C hands to Go)

**`struct device_metadata`**: read once at startup.

| Field | Meaning |
|---|---|
| `index` | NVML index (metadata only, not identity) |
| `uuid[96]`, `name[128]`, `pci_bus_id[]`, `driver_version[96]` | static identity |
| `valid_fields` | bitmask `DEVICE_META_UUID / NAME / PCI_BUS_ID / DRIVER_VERSION` |
| `nvml_error` | the **first** NVML error seen, kept for diagnostics |

**`struct device_snapshot`**: one poll of one GPU (looked up by UUID).

| Field | Notes |
|---|---|
| `uuid`, `index` | identity + current index |
| `gpu_util`, `mem_util` | percent |
| `mem_used`, `mem_free`, `mem_total` | bytes (`ULLONG_MAX` is treated as not available) |
| `temp_c`, `power_mw`, `sm_clock_mhz`, `mem_clock_mhz`, `power_limit_mw` | |
| `throttle_reasons` | bitmask (`ClocksEventReasons` on NVML ≥ 13, `ClocksThrottleReasons` before) |
| `ecc_{corrected,uncorrected}_{volatile,aggregate}` | four ECC counters |
| `valid_fields` | one bit per field above (`DEVICE_VALID_*`) |
| `valid`, `nvml_error` | handle obtained? / first error |

The important idea is **`valid_fields`**: an unsupported metric stays zero in C but its bit is not set, so a real `0` and "not reported" are never confused.

**`struct process_snapshot`**: processes on one GPU.

- `entries[]` of `struct process_entry { pid, used_gpu_memory, source_flags, memory_valid }`.
- Three sources are queried and **merged by PID**: compute, graphics, MPS. `source_flags` records which lists named the PID. If the same PID shows up with different VRAM values, the larger one is kept.
- `used_gpu_memory == ULLONG_MAX` (`NVML_VALUE_NOT_AVAILABLE`) sets `memory_valid = 0` instead of storing a huge number.
- The list size is queried first, then retried up to 5 times with a bigger buffer.
- **`complete`** is true only if at least one source succeeded and each other source either succeeded or returned `NOT_SUPPORTED`. This flag exists so the daemon never concludes "that process exited" from a partial list.
- `truncated` is for bounded consumers; the C layer never sets it.

### 3.3 Xid events (`xid.c`)

- One NVML event set, registered for `nvmlEventTypeXidCriticalError` on every enumerated GPU. The UUIDs are remembered so the set can be rebuilt after an NVML reinit.
- `xid_event_set_wait` blocks up to `timeout_ms`. On success it fills `struct xid_event`: timestamp (`CLOCK_REALTIME`, taken when wait returns), `device_uuid`, `device_index`, `nvml_event_type`, `nvml_event_data` (the Xid code).
- Returns `XID_WAIT_OK / TIMEOUT / ERROR / NOT_SUPPORTED`.
- `associated_pid` exists in the struct but `xid.c` never fills it, and `xid_handle_event` is a stub. I did not see Go code that stores Xids **[not seen]**.

---

## 4. Source 2: eBPF (kernel layer)

Files: `common.h` (shared ABI), `cuda_actions.bpf.c`, `host_ctx.bpf.c`, `driver_kprobes.bpf.c`, `proc_lifecycle.bpf.c`, `gpu_sm.bpf.c`.

### 4.1 The problem it solves: "which GPU is this call for?"

CUDA calls carry no GPU UUID. The kernel only sees a thread calling `cuMemAlloc`. The *device ordinal* is whatever the process sees under `CUDA_VISIBLE_DEVICES`, so ordinal 0 in one process is not ordinal 0 in another.

The BPF side therefore tracks bindings, all stamped with the process start time so recycled PIDs cannot inherit them:

```
cuCtxCreate_v2/v3/v4, cuDevicePrimaryCtxRetain
        │  (uretprobe reads the new ctx pointer)
        ▼
ctx_to_device   key {tgid, ctx pointer} → {device_ordinal, start_boottime_ns}
        │
cuCtxSetCurrent / PushCurrent / (create) → bind_context()
        ▼
tid_to_device   key {pid_tgid} → same binding          (per thread)
        │
current_device_ordinal() / init_event()
        ▼
event.device_ordinal   (or 0xffffffff = WEDJAT_UNKNOWN_DEVICE)
```

`cuCtxPopCurrent` clears the thread binding, because the BPF side keeps no context stack. Calls in that gap are reported as "device unknown". `pid_to_device` is a per-process fallback, flagged `EVENT_F_FROM_PID_FALLBACK` when used.

`start_boottime_ns` (from `task->start_boottime`) is checked on every lookup. A mismatch deletes the stale entry.

### 4.2 Two paths: aggregate vs. ring buffer

| Path | Used for | Where it goes |
|---|---|---|
| **Aggregate** (hot) | launch, memcpy, alloc, free, sync, ioctl, mmap, uvm_* | `agg_map`, a per-CPU hash, incremented in kernel. Nothing is sent per call. |
| **Event** (rare, important) | ctx create/destroy, proc exec/exit, syncs slower than `sync_stall_us`, **failed** ioctl/uvm_ioctl/mmap | `events_pipe`, a 256 KB ring buffer |

With `raw_capture` enabled (`CONFIG_F_RAW_CAPTURE`) every hot-path event goes to the ring buffer instead of being counted. That is more exact and much more expensive.

Calls that have an entry and an exit (alloc, free, sync, driver calls) are paired through three bounded LRU "inflight" maps keyed by `{pid_tgid, api_id}`, so an abandoned call ages out instead of leaking. The device ordinal is captured **at call entry**, not exit, because the thread may switch context in between.

Allocations are also remembered in `alloc_map` (`{tgid, address}` → bytes, device) so a later `cuMemFree` can be credited with the right size and device. The code comment states this is for counters, **not** VRAM truth.

### 4.3 Wire structs

**`struct event`**: exactly 64 bytes (`_Static_assert`). Go mirrors it as `bpfEvent` and decodes little-endian.

| Offset | Field | Meaning |
|---|---|---|
| 0 | `ts_ns` | `bpf_ktime_get_ns()` |
| 8 | `start_boottime_ns` | process generation (only set for exec/exit) |
| 16 | `latency_ns` | call duration |
| 24 | `address` | pointer, ioctl cmd, VA, etc. depending on API |
| 32 | `bytes` | |
| 40 | `tgid` | |
| 44 | `tid` | |
| 48 | `device_ordinal` | CUDA-visible ordinal, **not** a physical GPU |
| 52 | `api_id` | enum `event_id` |
| 56 | `flags` | `DEVICE_UNKNOWN`, `FROM_PID_FALLBACK`, `RAW_CAPTURE` |
| 60 | `status` | return code / exit code |

**`event_id`**: `CTX_SET=1, CTX_CREATE=2, CTX_DESTROY=3, LAUNCH=4, ALLOC=5, FREE=6, MEMCPY=7, SYNC=8, UVM_FAULT=9, UVM_MIGRATE=10, UVM_EVICT=11, IOCTL=12, MMAP=13, SM_BLOCK_START=14, SM_BLOCK_END=15, PROC_EXEC=16, PROC_EXIT=17, UVM_IOCTL=18, CTX_POP=19`. These values are a stable contract.

**`agg_key`** `{tgid, device_ordinal, api_id, pad}` → **`agg_val`** (per CPU):
`count, bytes, latency_sum_ns, latency_max_ns, alloc_bytes, free_bytes, errors, uvm_faults, uvm_evicts`.

**`stats_val`**, one slot per event ID: `ringbuf_drops, map_update_failures, unknown_device_events, alloc_free_misses`. These are the kernel's self-report of whether tracing is keeping up.

**`config_val`** `{flags, sync_stall_us}`: written by the daemon from `tracing.raw_capture` and `tracing.sync_stall_us`.

### 4.4 Maps at a glance

| Map | Type | Size | Pinned? |
|---|---|---|---|
| `events_pipe` | ringbuf | 256 KB | no |
| `agg_map` | per-CPU hash | 8192 | no |
| `stats_map` | per-CPU array | 20 | no |
| `config_map` | array | 1 | no |
| `*_inflight_map` (ctx / cuda / driver) | LRU hash | 8192 each | no |
| `tid_to_device` | LRU hash | 10240 | **yes** |
| `ctx_to_device` | LRU hash | 4096 | **yes** |
| `alloc_map` | LRU hash | 32768 | **yes** |
| `pid_to_device` | hash | 16384 | no |
| `seen_processes` | LRU hash | 16384 | no |

Pinned maps live under `/sys/fs/bpf/wedjat/v2` so a daemon restart keeps "which thread owns which context". The directory is versioned so a layout change does not read stale state.

`seen_processes` is filled the first time a process produces any GPU telemetry. `proc_lifecycle` only reports exec/exit for processes in it, so unrelated processes cost nothing.

### 4.5 The probes

| Object | Attach point | Records |
|---|---|---|
| `cuda_actions` | uprobe/uretprobe on `libcuda`: `cuMemAlloc*`, `cuMemFree*`, `cuMemcpy*`, `cuLaunchKernel*`, `cuGraphLaunch`, `cu*Synchronize` | ALLOC, FREE, MEMCPY, LAUNCH, SYNC |
| `host_ctx` | uprobes: `cuCtxCreate_*`, `cuDevicePrimaryCtxRetain`, `cuCtxSetCurrent`, `Push/PopCurrent`, `cuCtxDestroy_v2` | device bindings, CTX_CREATE/DESTROY |
| `driver_kprobes` | kprobe/kretprobe: `nvidia_mmap`, `nvidia_ioctl`, `uvm_ioctl`, `uvm_va_block_service_fault`, `uvm_migrate`, `uvm_va_block_evict_pages` | MMAP, IOCTL, UVM_* |
| `proc_lifecycle` | tracepoints `sched_process_exec`, `sched_process_exit` | PROC_EXEC, PROC_EXIT (exit only when the **last thread** leaves) |
| `gpu_sm` | runs **inside the GPU** via bpftime, not in the kernel | `dev_event` (32 bytes): per-block start/end with `sm_id` and GPU timer. Not in `bpfObjects`, so `LoadTracer` does not load it. |

The NVIDIA driver is not a stable ABI. `attach.go` keeps an alias table (for example `nvidia_ioctl` → `nvidia_unlocked_ioctl`) and attaches to the first symbol that exists. Attach is best effort: failures are listed in `Tracer.Failed` and logged grouped by cause, and the tracer only fails outright if **nothing** attached.

---

## 5. Userspace: from kernel counters to database rows

### 5.1 Startup (`run.go`)

1. `daemon.Run` creates the default tracer config and sets its drain interval from `polling.ebpf_drain_tick_ms`.
2. Find libcuda and the `.bpf.o` directory. The default tracer config enables the optional execute-bit fix for libcuda, because uprobes need it and match by inode.
3. `LoadTracer`: merge the four objects into one collection (a map declared by several objects is shared once), reopen compatible pinned maps, attach everything it can.
4. `bindDevices`: for every NVML device with a UUID, look up its DB row (`GPUIDByUUID`) and give that list to `tracerIdentity`.
5. `SetConfig`, then start `drainLoop` and `consumeEvents`. Both push
   aggregate/incident telemetry through `source.Source`.

If any step fails, the daemon keeps running on NVML alone.

### 5.2 The 1-second drain (`agg.go`)

```
for each key in agg_map:
    LookupAndDelete(key)                 atomic: update lands in this drain or the next
    value  = mergeAgg(per-CPU values)    counters SUM, latency_max takes the MAX
    group  = (tgid, ordinal)
    procID = ids.processID(tgid)         /proc → UpsertProcess
    gpuID  = ids.gpuID(tgid, ordinal)    CUDA ordinal → physical GPU row
    if either failed → count in `unattributed`, skip
    applyAPI(row, api_id, value)         fold this API into the (process, GPU) row
rows sorted by (process, gpu)
backlog.Add(at, rows); backlog.Flush(pushAggregates)
pushAggregates wraps rows in TypeEBPFAggregate and calls Source.PushEBPFEvent
```

Kernel rows are per-API; the database keys by `(ts, proc_id, gpu_id)`, so all APIs of one process on one GPU are merged into one row.

`applyAPI` mapping:

| `api_id` | `agg` columns updated |
|---|---|
| LAUNCH | `launches += count` |
| MEMCPY | `memcpy_calls += count`, `memcpy_bytes += bytes` |
| ALLOC | `alloc_calls += count`, `alloc_bytes += alloc_bytes` |
| FREE | `free_bytes += free_bytes` |
| SYNC | `sync_calls += count`, `sync_us_sum += latency_sum/1000`, `sync_us_max = max(...)` |
| IOCTL | `ioctl_calls += count` |
| *(any API)* | `errors`, `uvm_faults`, `uvm_evicts` |

The tracer backlog calls `pushAggregates`, which places a message on the
bounded, non-blocking source queue. That enqueue currently reports success
even if the queue is full and the message is dropped, so the backlog cannot
retry that particular drop.

### 5.3 The ring buffer consumer (`events.go`)

Runs continuously, because if userspace falls behind the kernel drops records and increments `ringbuf_drops`.

| Event | What the daemon does |
|---|---|
| `PROC_EXEC` | `processID(tgid)`: register the process row immediately, so a short-lived process is identified before its counters arrive |
| `PROC_EXIT` | If the cached row matches the exit's `start_boottime_ns`: `EndProcess(procID, ts, "exit")`. Then delete only that generation's entries from `tid_to_device`, `pid_to_device`, `ctx_to_device`, `alloc_map`. Then `ids.forget`. |
| `SYNC` (slow) | If `latency ≥ sync_stall_us` (default 250 000 µs): push a tagged `sync_stall` incident through the source |

Separately, every drain tick `scanHungSyncs` walks `cuda_inflight_map`. A SYNC
entry older than 2 000 000 µs (2 s) means the thread never came back; the
tracer pushes a tagged `sync_hang` incident through the source. The DB writer
persists both incident types. Process registration and exit handling still
call database methods directly for identity/lifecycle bookkeeping.

### 5.4 Identity bridge (`ids.go`)

| Kernel knows | DB wants | How it is bridged |
|---|---|---|
| `tgid` (+ `start_boottime_ns` on exit) | `proc_id` | `ReadProcessIdentity` reads `/proc/<pid>/stat` field 22 (`starttime`, in clock ticks) and the command name. `UpsertProcess(boot_id, tgid, start_ticks, …)` returns the same row the NVML path made. A cache is validated against `/proc` on each use. If the process is already gone, it is reported unresolvable, never guessed. |
| CUDA ordinal (per process) | `gpu_id` | Read `/proc/<tgid>/environ` for `CUDA_VISIBLE_DEVICES`, map ordinals to the known GPU UUIDs, cache per process generation. Ambiguous mappings are left **unattributed** instead of falling back to NVML's physical index. |
| `start_boottime_ns` ↔ `start_ticks` | exit matching | `startTicksMatchBoottime` converts between the two clocks so an exit event only closes the right process generation. |

An unresolvable device is stored as **NULL**, never as GPU 0, so an incident is never pinned to the wrong card.

---

## 6. The NVML path into the database (`core/source/nvml`, `core/writer.go`)

### 6.1 The adaptive sampler

The source starts an `NVMLSampler` that polls every configured GPU UUID and
emits one `nvml.Sample` per tick. With no clients it polls at
`nvml_db_tick_ms`. With clients it polls at the faster of the configured DB and
socket ticks. The dispatcher independently gates DB and socket output so each
uses its configured cadence. The sample includes GPU readings, process VRAM
readings, and a `ProcessesComplete` flag so an incomplete poll does not close
processes.

### 6.2 `Record(ctx, db, bootID, message)`

`core.Record` switches on `TelemetryMessage.Type`:

1. **NVML sample:** valid GPU fields are written to the GPU minute bucket. Unreported fields are stored as SQL `NULL`. Process readings update the VRAM ledger; a complete process list also closes processes absent from the sample.
2. **eBPF aggregate:** rows are written using the message timestamp.
3. **eBPF incident:** the incident is inserted or merged into its deduplicated record.

For NVML processes, `recordProcesses`:
   - For every NVML process: read `/proc` identity (skip if it vanished), `UpsertProcess`, `GPUIDByUUID`, skip if `!VRAMValid`, collect a `ProcessVRAM` row.
   - `UpsertProcessVRAM(rows)` updates `proc_gpu` (first/last seen, peak and last VRAM).
   - **Sweep:** if `ProcessesComplete`, `CloseProcessesNotSeen(bootID, seen, now)` closes every open process that NVML no longer lists. The daemon has no exit events from NVML, so this is how NVML-only processes end.
   - Guard: an empty list that is also incomplete returns early (nothing safe to close). An empty list that is complete still sweeps, because that means everything exited.

`RegisterDevices` runs once at startup and upserts each GPU into `gpus` (UUID, index, name, PCI bus ID, driver version, VRAM total).

---

## 7. Database schema

There are two SQLite files: a long-lived **meta** file (`meta.sql`) for identities and incidents, and **daily** files (`daily.sql`) for the high-volume time series. The split and the daily rotation are **[inferred]** from the file names and `retention.day_files_days`; the Go `db` package is **[not seen]**.

### 7.1 `meta.sql`: identities and events

**`gpus`**: one row per physical GPU.

| Column | Notes |
|---|---|
| `gpu_id` PK | internal id used everywhere else |
| `uuid` UNIQUE NOT NULL | the real identity |
| `idx`, `name`, `pci_bus_id`, `vram_total_bytes`, `driver_version` | metadata |
| `parent_gpu_id` → `gpus` | for sub-devices (for example MIG) **[inferred]** |
| `first_seen_ts`, `last_seen_ts` | |

**`procs`**: one row per process **instance**.

| Column | Notes |
|---|---|
| `proc_id` PK AUTOINCREMENT | |
| `boot_id`, `tgid`, `start_ticks` | `UNIQUE (boot_id, tgid, start_ticks)`, which is the PID-reuse-safe identity |
| `command`, `cmdline`, `container` | |
| `first_seen_ts`, `end_ts`, `end_reason` | `end_ts IS NULL` means still running (partial index `procs_running`) |
| `exit_code`, `term_signal` | |

**`proc_devices`** `(proc_id, ordinal) → gpu_id`: the per-process CUDA ordinal map. `WITHOUT ROWID`. I did not see Go code that writes it **[not seen]**.

**`proc_gpu`** `(proc_id, gpu_id)`: `first_seen_ts`, `last_seen_ts`, `peak_vram_bytes`, `last_vram_bytes`. Fed by the NVML path.

**`incidents`**: `incident_id`, `type` (`sync_stall`, `sync_hang`, …), `proc_id` (nullable), `gpu_id` (nullable), `first_ts`, `last_ts`, `occurrences`, `dedupe_key`, `summary`, `detail`, `dump_id`. Repeats within the dedupe window collapse into one row with a rising `occurrences`. Indexed by `last_ts` and `(dedupe_key, last_ts)`.

**`dumps`**: `trigger_key`, `created_ts`, `path`, `size_bytes`. Capped by `retention.max_dumps`.

**`daemon_log`** `(ts, level, kind, message)` and **`daemon_state`** `(k, v)`: the daemon's own log and key/value state.

### 7.2 `daily.sql`: time series

**`gpu_samples`**: PK `(ts, gpu_id)`, `ts` is the minute bucket.

`n` (samples folded in), `util_gpu_sum`, `util_gpu_max`, `util_mem_sum`, `temp_max`, `power_mw_sum`, `vram_used_max`, `sm_clock_max`, `mem_clock_max`, `power_limit_mw`, `throttle_or`, `ecc_errors`.

The `_sum` columns together with `n` give averages; `_max` columns keep peaks; `throttle_or` is the OR of all throttle bitmasks seen in the minute. All value columns are nullable, so "never reported" stays NULL.

**`agg`**: PK `(ts, proc_id, gpu_id)`.

`launches`, `memcpy_calls`, `memcpy_bytes`, `alloc_calls`, `alloc_bytes`, `free_bytes`, `sync_calls`, `sync_us_sum`, `sync_us_max`, `ioctl_calls`, `uvm_faults`, `uvm_evicts`, `errors`, `vram_used_bytes` (nullable).

---

## 8. The socket (`core/socket.go`)

- Unix socket at a configured path. A stale file is removed first.
- Mode **0660**. If `SocketOptions.Group` is set, the file is chowned to that group when possible.
- The server reads `SocketChannel` once and broadcasts each telemetry message to all connected clients as newline-delimited JSON.
- Each write has a 5 s deadline. A disconnected or slow client is closed.
- `Stop()` cancels the context, closes the listener, waits for all goroutines, and removes the socket file.

One line on the wire looks like:

```json
{
  "type": "nvml_sample",
  "timestamp_unix_nano": 1790000000000000000,
  "nvml": {
    "UnixNano": 1790000000000000000,
    "MinuteUnix": 1789999980,
    "GPUs": [ /* NVML GPU readings */ ],
    "Processes": [ /* NVML process VRAM readings */ ],
    "ProcessesComplete": true
  }
}
```

The socket carries NVML samples, eBPF aggregates, and eBPF incidents while at
least one client is connected. Socket client count controls NVML polling and
socket delivery; it does not change the DB output cadence.

---

## 9. Configuration (`core/config.go`)

The YAML file groups settings under `storage` and `polling`:

| Section | Keys (defaults) |
|---|---|
| `storage` | `reset_on_boot` (true), `day_files_days` (30), `processes_days` (90), `incidents_days` (90) |
| `polling` | `nvml_db_tick_ms` (2000), `nvml_socket_tick_ms` (500), `ebpf_drain_tick_ms` (1000) |

NVML tick values must be greater than zero; the eBPF drain tick may be zero to
select the tracer default.

---

## 10. Routing summary

- NVML samples go to `DBChannel`; while clients are connected, they also go to
  `SocketChannel` at the socket tick. The DB receives NVML at its own configured
  tick, even while faster socket samples are flowing.
- eBPF aggregates and incidents always go through `DBChannel`; while clients
  are connected, they also go through `SocketChannel`.
- eBPF process identity and lifecycle bookkeeping still invokes database
  operations directly; it is separate from aggregate/incident telemetry routing.
- The producer-to-consumer channels are bounded and non-blocking. A full queue
  can drop a message rather than block the sampler or event consumer.

---

## 11. Files this is based on

`daemon/internal/daemon/daemon.go`; `daemon/core/source/source.go` and
`telemetry.go`; `daemon/core/source/nvml`; `daemon/core/source/ebpf`; and
`daemon/core/socket.go`, `writer.go`, and `db`.
