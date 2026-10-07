# Wedjat Developer Guide

This document describes how to build, develop, and contribute to Wedjat.

## Architecture Overview

Wedjat consists of three main components:

1. **`wedjatd`** - The GPU monitoring daemon (runs as root)
   - Collects NVML telemetry (GPU utilization, memory, temperature, power)
   - Optionally loads eBPF programs for CUDA API tracing
   - Stores data in SQLite databases (per-day files + metadata)
   - Exposes data via Unix socket (`/run/wedjatd/socket`)

2. **`wedjat`** - The web UI server (runs as root, binds to 127.0.0.1:3000)
   - Serves the embedded React frontend
   - Provides REST API (`/api/*`) backed by read-only SQLite connections
   - Bridges WebSocket (`/socket`) to daemon socket for live updates
   - Embeds frontend assets via `go:embed`

3. **Frontend** - React + Vite application
   - Built from `ui/web/`
   - Outputs to `ui/server/httpd/dist/`
   - Embedded into `wedjat` binary at build time

## Prerequisites

- **Go** 1.21+ (for daemon and CLI)
- **Node.js** 20+ (for frontend build)
- **npm** 9+ (for frontend dependencies)
- **Linux** with NVIDIA GPU + drivers (for runtime)
- **Clang/LLVM** 14+ (for eBPF compilation, optional)

## Quick Start (Development)

```bash
# Build everything
sudo make

# Run daemon in one terminal
sudo make dev-daemon

# Run UI server in another terminal
sudo make dev-ui

# Or run both (daemon runs in background)
sudo make dev
```

The web UI will be available at `http://127.0.0.1:3000`

## Project Structure

```
wedjat/
├── cmd/
│   └── wedjat/              # Main CLI entry point
├── daemon/
│   ├── core/                # Core daemon logic
│   │   ├── db/              # SQLite read/write layer
│   │   ├── source/          # Telemetry sources (NVML, eBPF)
│   │   └── socket/          # Unix socket protocol
│   └── wedjatd/             # Daemon main
├── internal/
│   ├── daemon/              # Shared daemon internals
│   └── wire/                # Socket message types (PascalCase JSON)
├── ui/
│   ├── frontend/            # React source (Vite)
│   └── server/              # Go HTTP+WS server
│       ├── client/          # Daemon socket client
│       ├── data/            # Read-only DB layer for HTTP API
│       └── httpd/           # HTTP server with embedded assets
├── kernels_to_trace/        # eBPF C source
├── scripts/                 # Install/uninstall scripts
├── dist/                    # Release artifacts (gitignored)
└── docs/                    # Documentation
```

## Building

### Release Build

```bash
sudo make
```

This produces a tarball at `dist/wedjat-linux-<arch>.tar.gz` containing:
- `wedjat` - Web UI server + CLI
- `wedjatd` - GPU monitoring daemon
- `config.yaml` - Default configuration
- `wedjatd.service` - systemd unit
- `ebpf/*.bpf.o` - Compiled eBPF objects

### Development Build (without eBPF)

```bash
# Build just the Go binaries
go build -o wedjat ./cmd/wedjat
go build -o wedjatd ./daemon/cmd/wedjatd

# Build frontend
cd ui/web && npm ci && npm run build

# Run with dev paths
sudo ./wedjat --dev
# Or manually:
sudo ./wedjat --web --port=3000 --socket=./dev/run/wedjat.sock --data=./dev/var/lib/wedjat
```

## Running Tests

```bash
# All tests
go test ./...

# Daemon tests only
go test ./daemon/...

# Database read-only layer tests
go test ./daemon/core/db/...
```

## Configuration

The daemon reads `/etc/wedjat/config.yaml`:

```yaml
storage:
  path: /var/lib/wedjat          # Database directory
  lock_path: /run/wedjat/daemon.lock
  reset_on_boot: true
  max_size_bytes: 524288000      # 500MB total
  min_free_bytes: 1073741824     # 1GB minimum free

retention:
  day_files_days: 30             # Keep daily DB files
  processes_days: 90             # Keep process metadata
  incidents_days: 90             # Keep incident records
  max_dumps: 20                  # Max coredump files

tracing:
  enabled: true                  # eBPF tracing
  raw_capture: false             # Capture all events (verbose)
  sync_stall_us: 0               # Sync stall threshold (0 = default 250ms)
  fix_libcuda_permissions: true  # Make libcuda executable for uprobes
```

## Socket Protocol

The daemon communicates via Unix socket (`/run/wedjatd/socket`) using JSON envelopes:

```json
{
  "type": "gpu|procs|xid|agg|event",
  "timestamp_unix_nano": 1234567890123456789,
  "data": { ... }
}
```

Message types (in `internal/wire/wire.go`):
- `gpu` - GPU telemetry sample (`GPUSample`)
- `procs` - Process list (`ProcList` with `ProcessSample[]`)
- `xid` - NVIDIA Xid error event
- `agg` - Aggregated CUDA counters (`AggRow`)
- `event` - Raw eBPF event (`Event`)

All JSON fields use PascalCase (e.g., `TsNano`, `UtilGPU`, `VRAMBytes`).

## Database Schema

Two SQLite databases:
- **`meta.db`** - Persistent metadata (GPUs, processes, incidents)
- **`<date>.db`** - Daily telemetry (GPU samples, aggregates)

Both use WAL mode for concurrent read/write access.

Key tables:
- `gpus` - GPU identity (UUID, name, PCI bus, VRAM, driver)
- `procs` - Process instances (boot_id, TGID, command, timestamps)
- `proc_gpu` - Per-process GPU VRAM usage
- `gpu_samples` - Per-minute GPU telemetry (sums + extrema)
- `agg` - Per-minute per-process CUDA aggregates
- `incidents` - Detected anomalies (Xid, hangs, OOM, etc.)

## eBPF Development

The eBPF programs live in `kernels_to_trace/`:

- `cuda_actions.bpf.c` - CUDA API tracing (memcpy, alloc, free, sync)
- `driver_kprobes.bpf.c` - Kernel probe for Xid events
- `proc_lifecycle.bpf.c` - Process fork/exit tracking
- `host_ctx.bpf.c` - Host context helpers

Compile with:
```bash
cd daemon
make -C ebpf
```

Requires kernel headers and Clang/LLVM.

## Adding New REST Endpoints

1. Add response type in `ui/server/data/data.go`
2. Add data access method in `ui/server/data/data.go`
3. Add handler in `ui/server/httpd/httpd.go`
4. Register route in `httpd.go` `Start()` method

Example:
```go
// In data.go
type MyNewResponse struct {
    Field1 string `json:"field1"`
    Field2 int64  `json:"field2"`
}

func (d *Data) GetMyData(ctx context.Context) ([]MyNewResponse, error) {
    // query DB
}

// In httpd.go
func (s *Server) handleMyData(w http.ResponseWriter, r *http.Request) {
    data, err := s.data.GetMyData(r.Context())
    if err != nil { http.Error(w, err.Error(), 500); return }
    s.writeJSON(w, data)
}

// In Start()
mux.HandleFunc("/api/mydata", s.handleMyData)
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make changes with tests
4. Run `gofmt` and `clang-format` (for C code)
5. Ensure all tests pass: `go test ./...`
6. Submit PR

### Code Style

- Go: `gofmt` (enforced in CI)
- C (eBPF): `clang-format` (LLVM style)
- JavaScript: Prettier (enforced via Vite)

## Release Process

1. Update version in `cmd/wedjat/main.go` (or use git tags)
2. Run `sudo make` to create release tarball
3. Verify: `tar -tzf dist/wedjat-linux-amd64.tar.gz`
4. Create GitHub release and upload artifacts
5. Update install script checksums if needed

## Debugging

### Daemon Logs
```bash
sudo journalctl -u wedjatd -f
```

### Web Server Logs
The web server logs to stdout/stderr when run manually:
```bash
sudo wedjat --web
```

### Socket Inspection
```bash
# Connect to daemon socket
socat - UNIX-CONNECT:/run/wedjatd/socket
```

### Database Inspection
```bash
sqlite3 /var/lib/wedjat/meta.db ".tables"
sqlite3 /var/lib/wedjat/meta.db "SELECT * FROM gpus;"
```

## Common Issues

### "UI not built" error
```bash
cd ui/web && npm ci && npm run build
go build ./cmd/wedjat
```

### "Permission denied" on libcuda.so.1
```bash
sudo chmod a+x /usr/lib/x86_64-linux-gnu/libcuda.so.1
# Or set fix_libcuda_permissions: true in config.yaml
```

### eBPF programs fail to load
- Check kernel version (5.10+ required for CO-RE)
- Check `dmesg` for BPF verifier errors
- Daemon falls back to NVML-only mode automatically

### Port 3000 already in use
```bash
sudo wedjat --web --port=3001
# Or change in config
```

## License

Apache 2.0 - see LICENSE file.