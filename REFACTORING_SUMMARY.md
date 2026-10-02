# Code Refactoring Summary - 2026-10-02

## Database Layer Cleanup

### Before:
```
db/
  ├── db.go              (200 lines)
  ├── schema.go          (60 lines)   ← tiny helper
  ├── timer.go           (50 lines)   ← tiny helper  
  ├── dayfiles.go        (75 lines)   ← tiny helper
  ├── queries.go         (300 lines)
  ├── aggregates.go      (150 lines)
  ├── processes.go       (150 lines)
  └── sql/
```

### After:
```
db/
  ├── db.go              (350 lines) ← merged lifecycle
  ├── queries.go         (300 lines)
  ├── aggregates.go      (150 lines)
  ├── processes.go       (150 lines)
  └── sql/
```

**Result**: 7 files → 4 files

---

## Core Package Restructuring

### Before:
```
core/
  ├── bridge.c, bridge.h     ← NVML-related (misplaced)
  ├── sources.go             ← NVML-related (unclear name)
  ├── tracer/                ← eBPF-related (unclear name)
  ├── config.go              
  ├── socket.go              
  ├── writer.go              
  ├── snapshot.go            
  └── db/                    
```

### After:
```
core/
  ├── nvml/                  ← All NVML integration
  │   ├── bridge.c           (C wrapper)
  │   ├── bridge.h           (header)
  │   └── nvml.go            (Go interface)
  │
  ├── ebpf/                  ← All eBPF integration
  │   ├── tracer.go          (core types)
  │   ├── load.go            (load BPF objects)
  │   ├── attach.go          (attach probes)
  │   ├── run.go             (session lifecycle)
  │   ├── events.go          (ring buffer consumer)
  │   ├── agg.go             (aggregate drainer)
  │   └── ids.go             (identity mapping)
  │
  ├── db/                    ← Database layer
  │   ├── db.go              (connection + lifecycle)
  │   ├── queries.go         (CRUD operations)
  │   ├── aggregates.go      (counters + incidents)
  │   └── processes.go       (process lifecycle)
  │
  ├── config.go              ← General daemon stuff
  ├── socket.go
  ├── writer.go
  └── snapshot.go
```

**Result**: Clear data source separation

---

## What Changed

### Package Renames:
- `core/tracer` → `core/ebpf` (clearer purpose)
- `sources.go` → `nvml/nvml.go` (clearer location)

### Files Moved:
- `core/bridge.{c,h}` → `nvml/bridge.{c,h}`
- `core/sources.go` → `nvml/nvml.go`
- `core/tracer/*` → `core/ebpf/*`

### Files Merged:
- `db/schema.go` + `db/timer.go` + `db/dayfiles.go` → `db/db.go`

### Import Path Updates:
- `"daemon/core"` calls now split into:
  - `"daemon/core/nvml"` for GPU polling
  - `"daemon/core/ebpf"` for eBPF tracing
  - `"daemon/core"` for common utilities

---

## Architecture Now Clear

### Three Data Sources:
1. **NVML** (`core/nvml/`) - GPU metrics via NVIDIA library
2. **eBPF** (`core/ebpf/`) - CUDA API calls via kernel probes
3. **/proc** (`core/writer.go`) - Process metadata

### Data Flow:
```
NVML  ───┐
eBPF  ───┼──→ [core/writer.go] ──→ [core/db/] ──→ SQLite
/proc ───┘                             │
                                       └──→ [core/socket.go] ──→ Unix socket
```

---

## Benefits

1. **Clarity**: Each subdirectory has one clear purpose
2. **Discoverability**: Easy to find NVML vs eBPF code
3. **Modularity**: Data sources are independent
4. **Simplicity**: Fewer tiny helper files
5. **Maintainability**: Related code lives together

---

## Bugs Fixed During Refactoring

### Database Layer:
- ✅ Added 10s timeout to checkpoint (prevents shutdown hang)
- ✅ Fixed missing error cleanup on schema failure
- ✅ Added mutex documentation (caller must hold db.mu)
- ✅ WAL removal errors now logged
- ✅ Incident dedupe key length validated (max 512 bytes)

### Next Steps:
- Continue fixing bugs from DAEMON_CODE_REVIEW.md
- Review and simplify remaining core files
- Address race conditions and edge cases

---

## Build Status

✅ `go build ./cmd/wedjatd` - **SUCCESS**  
✅ `go build ./cmd/wedjat-verify-trace` - **SUCCESS**

All imports updated, all packages compile.
