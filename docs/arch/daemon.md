# wedjatd main.go Architecture

This outlines the structure, startup sequence, and developer guardrails for `daemon/cmd/wedjatd/main.go`.

## 1. High-Level Structure

```mermaid
graph TD
    A[main.go] --> B{Flags parsed}
    B -- "--dev" --> C[Dev Mode Configuration]
    B -- no flags --> D[System Mode Configuration]
    
    C --> C1(Check if wedjatd.service is active)
    C1 -- Active --> E[FATAL: Stop service first]
    C1 -- Inactive --> F(Set local paths)
    
    D --> G(Set system paths)
    
    F --> H[Acquire Lock]
    G --> H
    H --> I[Bootstrap Runtime]
    
    I --> J[Stage 1: SQLite Store]
    I --> K[Stage 2: eBPF & NVML Collector]
    
    J --> L(Wait for SIGINT/SIGTERM)
    K --> L
    
    L --> M[Clean Shutdown]
```

## 2. Path Isolation (`--dev` vs System)

| Resource | System Mode | `--dev` Mode |
| :--- | :--- | :--- |
| **Config File** | `/etc/wedjat/config.yaml` | `./dev-config.yaml` |
| **Database Dir** | `/var/lib/wedjat` | `./dev-data` |
| **Lock File** | `/run/wedjat/daemon.lock` | `/tmp/wedjat-dev.lock` |
| **BPF Map Pin Path** | `/sys/fs/bpf/wedjat` | *(unpinned, kernel cleans up)* |

## 3. Startup Order

The daemon uses the existing `bootstrap.Start` logic to bring up stages in a safe, rollback-capable order:

1. **Flag Parsing & Dev Collision Guard**
   - Parse `--dev` and `--config`.
   - If `--dev` is set, invoke `systemctl is-active wedjatd.service`. If it returns success, **abort immediately** to prevent double-attaching eBPF probes.
   - Set up paths according to the mode.

2. **Locking**
   - Acquire the file lock (e.g., `/run/wedjat/daemon.lock` or `/tmp/wedjat-dev.lock`). This prevents two instances of the same mode from running.

3. **Stage 1: Store (`store.Open`)**
   - Opens the SQLite databases (metadata and daily).
   - Handles `reset_on_boot` logic and boot ID checks.
   - Writes the new heartbeat and clean shutdown state.

4. **Stage 2: Collector (`collector.Start`)**
   - This wraps the cgo bridge.
   - **NVML**: Initializes `nvmlInit()`.
   - **eBPF**: Loads the BPF skeletons (`driver_kprobes`, `cuda_actions`).
   - Attaches uprobes and kprobes.
   - Starts the background Go goroutine to poll the BPF perf buffers and NVML, and writes events to the `store`.

5. **Running State**
   - The main goroutine blocks on `os.Signal` (SIGTERM, SIGINT).

## 4. Shutdown Sequence (Reverse Order)

When `SIGINT` (Ctrl+C) or `SIGTERM` (systemd stop) is caught:

1. **Stage 2 Shutdown (Collector)**
   - Stop the polling goroutine.
   - Destroy BPF skeletons. Because eBPF programs are unpinned (in dev mode) or we explicitly tear them down via libbpf, they detach cleanly from the kernel.
   - Call `nvmlShutdown()`.

2. **Stage 1 Shutdown (Store)**
   - Checkpoint the SQLite WAL.
   - Write `clean_shutdown = 1`.
   - Close the database connections cleanly.

3. **Lock Release**
   - The lock file is released and closed.
