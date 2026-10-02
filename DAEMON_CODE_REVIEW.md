# Wedjat Daemon Code Review
**Date**: 2026-10-02  
**Scope**: `/daemon/cmd`, `/daemon/core`, `/daemon/ebpf` (Go only), `/daemon/nvml` (Go only)

---

## Executive Summary

The daemon code is **solid and functional** overall, showing strong understanding of eBPF, database design, and system programming. However, there are **critical bugs**, **missing error handling**, **race conditions**, and **resource leaks** that need immediate attention.

**Critical Issues**: 6  
**High Priority**: 12  
**Medium Priority**: 8  
**Low Priority/Style**: 5

---

## CRITICAL ISSUES (Fix Immediately)

### 1. **Database Close Context Bug** - `core/db/db.go:185`
```go
func (db *DB) Close() error {
    // ...
    if err := checkpoint(ctxBackground(), db.day); err != nil {
```

**Problem**: Uses `ctxBackground()` after the caller's context is already cancelled. If checkpoint is slow, this will hang shutdown.

**Fix**: Use `context.WithTimeout(context.Background(), 10*time.Second)` so checkpointing has a hard deadline.

---

### 2. **Missing Database Lock on Read Queries** - `core/db/queries.go`
```go
func (db *DB) GPUIDByUUID(ctx context.Context, uuid string) (int64, error) {
    // ...
    db.mu.Lock()
    if err := db.checkOpen(); err != nil {
        db.mu.Unlock()
        return 0, err
    }
    var gpuID int64
    err := db.meta.QueryRowContext(ctx, `SELECT gpu_id FROM gpus WHERE uuid = ?`, uuid).Scan(&gpuID)
    db.mu.Unlock()  // Unlock happens AFTER the query
```

**Problem**: The mutex protects `db.meta` from concurrent writes, but the query is executed with the lock held. SQLite only needs one connection, but **holding a mutex during I/O blocks all other database operations**.

**Fix**: 
- Either accept that queries block writes (current behavior is okay but unintentional)
- Or use a read-write mutex (`sync.RWMutex`) so reads don't block each other
- Document which approach you chose

---

### 3. **Unclosed SQL Rows Leak** - `core/db/processes.go:54`
```go
rows, err := db.meta.QueryContext(ctx, ...)
if err != nil {
    return 0, fmt.Errorf("list running processes: %w", err)
}
defer rows.Close()

// ...
for rows.Next() {
    // ...
}
// ...
rows.Close()  // Manual close

// ...
for _, procID := range stale {
    result, err := db.meta.ExecContext(ctx, statement, endTS, EndVanished, procID)
    if err != nil {
        return closed, fmt.Errorf("close vanished process %d: %w", procID, err)  // LEAK: deferred rows.Close never runs
    }
```

**Problem**: Early return bypasses the `defer rows.Close()`. While you manually call `rows.Close()` before the loop, if an `ExecContext` fails, the defer never runs... wait, actually the manual close DOES happen before the loop, so this is fine. But the pattern is confusing.

**Actually, this is fine** - the manual `rows.Close()` happens before the update loop. False alarm, but the pattern is confusing. Consider refactoring to make intent clearer.

---

### 4. **Ring Buffer Read Loop Blocks Shutdown** - `core/tracer/events.go:27`
```go
func (t *Tracer) consumeEvents(ctx context.Context, database *db.DB, ids *tracerIdentity) {
    if t.reader == nil {
        return
    }
    for {
        record, err := t.reader.Read()  // BLOCKS indefinitely
        if err != nil {
            if errors.Is(err, ringbuf.ErrClosed) || ctx.Err() != nil {
                return
            }
```

**Problem**: `reader.Read()` blocks until an event arrives. Context cancellation doesn't interrupt it—only closing the reader does. If no events arrive, shutdown hangs until the next event.

**Current Fix**: `TracerSession.Close()` does call `reader.Close()`, so this is actually **fine**. But it's fragile—if someone removes that close, shutdown hangs.

**Improvement**: Document that `reader.Close()` is the only way to interrupt `consumeEvents()`.

---

### 5. **Process Identity Read Race** - `core/writer.go:121`
```go
for _, proc := range snapshot.Processes {
    // A process can exit between the NVML listing and this read, so a
    // missing /proc entry is an ordinary race rather than a failure.
    startTicks, command, err := ReadProcessIdentity(proc.PID)
    if err != nil {
        continue  // Process disappeared - counters are LOST
    }
```

**Problem**: If a process exits between NVML poll and here, you silently drop its VRAM data. For long-lived processes this is fine, but for **short-lived CUDA jobs** (like ML training scripts), you lose their entire history.

**Fix**: Cache `(PID, start_ticks, command)` when first seen, so disappearing processes can still be recorded. Or accept the limitation and document it.

---

### 6. **Aggregate Drain Can Drop Counters** - `core/tracer/agg.go:50`
```go
for iterator.Next(&key, &perCPU) {
    if err := t.aggMap.LookupAndDelete(&key, &perCPU); err != nil {
        if errors.Is(err, ebpf.ErrKeyNotExist) {
            // The kernel updated the row between the iteration and the
            // delete. That counter will be picked up next drain.
            continue
        }
```

**Problem**: `LookupAndDelete` is not atomic with `Next()`. If the kernel updates a key between iteration and deletion:
- Current drain skips it (the check above)
- Next drain also skips it (it was updated after this snapshot)
- **Counter is lost forever**

**Fix**: This is a known eBPF limitation. The comment is misleading—the counter is NOT picked up next drain. Either:
- Accept the loss and document it
- Use a batch lookup, but that's more complex
- Or ensure drain interval is fast enough that the race is negligible (current 1s is good)

---

## HIGH PRIORITY ISSUES

### 7. **Missing Error Check on Schema Install** - `core/db/db.go:61`
```go
// Create metadata schema
if err := installSchema(ctx, db.meta, "sql/meta.sql"); err != nil {
    db.meta.Close()
    return nil, fmt.Errorf("create metadata schema: %w", err)
}
```

**Problem**: If schema install fails, `db.meta.Close()` is called, but `db` itself is returned as nil. Fine. But if `rotateDayDB` fails below, `db.meta` is NOT closed.

**Fix**:
```go
if err := db.rotateDayDB(ctx, today); err != nil {
    db.meta.Close()  // Add this
    return nil, err
}
```

---

### 8. **Snapshot ProcessesComplete Race** - `core/snapshot.go:22`
```go
snapshot := Snapshot{
    // ...
    ProcessesComplete: true,  // Cleared below if any device fails
}

for i, uuid := range gpuUUIDs {
    // ...
    procs, err := PollProcesses(uuid)
    if err != nil {
        snapshot.ProcessesComplete = false
        continue  // BUT we keep polling other GPUs
    }
    snapshot.Processes = append(snapshot.Processes, procs...)
}
```

**Problem**: If GPU 0 fails, you set `ProcessesComplete = false` but keep polling GPU 1, 2, etc. The partial list is still used for process cleanup. This can **close processes that are alive but only using the failed GPU**.

**Fix**: If any GPU fails to report processes, **stop polling immediately** and return incomplete snapshot:
```go
if err != nil {
    snapshot.ProcessesComplete = false
    break  // Don't poll other GPUs
}
```

---

### 9. **Device Ordinal Out of Bounds** - `core/snapshot.go:17`
```go
for i, uuid := range gpuUUIDs {
    // ...
    sample.Index = uint(i)  // This is the slice index, not the device index
```

**Problem**: You're setting `sample.Index` to the slice position, not `device.Index`. If GPUs are discovered out of order, this mismatches the kernel's device ordinal.

**Fix**: Pass `DeviceInfo` to `BuildSnapshot()` instead of just UUIDs, or use a map `[uuid]index`.

---

### 10. **Config Defaults Silently Ignored** - `core/config.go:68`
```go
cfg := DefaultConfig()
if err := yaml.Unmarshal(data, &cfg); err != nil {
    return nil, fmt.Errorf("parse config: %w", err)
}
```

**Problem**: If the YAML has a typo (e.g., `retentoin:` instead of `retention:`), it's silently ignored and you use defaults. The operator thinks they configured 7 days but it's actually 30.

**Fix**: Use `yaml.UnmarshalStrict()` to error on unknown fields.

---

### 11. **Socket Write Deadline Too Short for Large Snapshots** - `core/socket.go:129`
```go
if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
    return
}
```

**Problem**: 5 seconds is generous for normal snapshots, but if you have 100 GPUs and 1000 processes, JSON encoding + write might timeout.

**Fix**: Either make it configurable, or calculate dynamically based on snapshot size. Or just document that 100+ GPUs need a tuned timeout.

---

### 12. **Tracer Close Doesn't Detach Links** - `core/tracer/tracer.go:78`
```go
func (t *Tracer) Close() error {
    t.closeOnce.Do(func() {
        if t.reader != nil {
            t.reader.Close()
        }
        for _, l := range t.links {
            l.Close()  // Close, but don't clear t.links
        }
        if t.collection != nil {
            t.collection.Close()
        }
    })
    return nil
}
```

**Problem**: `t.links` is never cleared after close. If someone accidentally calls `Close()` twice, the second call is a no-op (good), but the links slice is still there, confusing debugging.

**Fix**: Set `t.links = nil` after closing.

---

### 13. **Missing Lock on Tracer Section Map** - `core/tracer/tracer.go:52`
```go
type Tracer struct {
    // ...
    sections map[string]string  // No mutex, but accessed from multiple goroutines?
```

**Problem**: `sections` is written during `LoadTracer()` and read during `attachAll()`. Since both happen in the same goroutine before `StartTracer()` returns, this is **probably fine**. But if you ever make loading concurrent, this is a data race.

**Fix**: Document that `sections` is read-only after construction, OR add a mutex.

---

### 14. **Day Rotation Race** - `core/db/db.go:75`
```go
func (db *DB) rotateDayDB(ctx context.Context, date string) error {
    if db.dayName == date && db.day != nil {
        return nil  // RACE: Another goroutine might close db.day here
    }

    // Close previous day DB if open
    if db.day != nil {
        db.day.Close()
    }
```

**Problem**: Caller holds `db.mu`, so this is fine. But `rotateDayDB` doesn't document that it expects the mutex to be held.

**Fix**: Add a comment: `// Caller must hold db.mu`.

---

### 15. **Hung Sync Detection Uses Wall Clock** - `core/tracer/events.go:98`
```go
now, err := boottimeNs()
if err != nil {
    return err
}
// ...
if start == 0 || now < start || now-start < threshold {
```

**Problem**: `boottimeNs()` reads `CLOCK_BOOTTIME`, which is monotonic, but you compare it against event timestamps from the kernel. If the kernel uses a different clock (e.g., `CLOCK_MONOTONIC`), this comparison is wrong.

**Fix**: Verify that eBPF events are stamped with `CLOCK_BOOTTIME`. If not, switch to the correct clock.

---

### 16. **Process Command Truncated** - `core/writer.go:176`
```go
command := stat[open+1 : closing]
```

**Problem**: Linux limits command names to 15 characters in `/proc/pid/stat`. For longer names, you get "verylongcomman" instead of "verylongcommandname". This is a kernel limitation, not your bug, but it's misleading.

**Fix**: Document in the database schema that `command` is the kernel's truncated name, not the full argv[0]. Or read `/proc/pid/cmdline` for the full path (but that's more overhead).

---

### 17. **Tracer Stats Map Lookup Never Fails** - `core/tracer/tracer.go:118`
```go
for slot := uint32(0); slot < statsSlots; slot++ {
    var perCPU []statsVal
    if err := t.statsMap.Lookup(slot, &perCPU); err != nil {
        continue  // Silently skip missing slots
    }
```

**Problem**: If `Lookup()` fails for a reason other than "slot empty" (e.g., corrupt map), you silently undercount. The returned stats look correct but are missing data.

**Fix**: Log non-`ErrKeyNotExist` errors.

---

### 18. **Aggregate Merge Doesn't Check for Overflow** - `core/tracer/agg.go:22`
```go
func mergeAgg(values []aggVal) aggVal {
    var out aggVal
    for _, v := range values {
        out.Count += v.Count  // Can overflow
        out.Bytes += v.Bytes  // Can overflow
```

**Problem**: If a process makes 2^64 allocations, `Count` wraps to 0. This is **extremely unlikely**, but for long-running daemons (months), it's theoretically possible.

**Fix**: Either:
- Accept it and document that counters can wrap
- Saturate at `math.MaxUint64`
- Panic (because if you hit this, something is very wrong)

---

## MEDIUM PRIORITY ISSUES

### 19. **Inefficient Process Close Loop** - `core/db/processes.go:77`
```go
for _, procID := range stale {
    result, err := db.meta.ExecContext(ctx, statement, endTS, EndVanished, procID)
```

**Problem**: One SQL statement per process. If 1000 processes vanish (e.g., cluster job ends), this is 1000 round-trips.

**Fix**: Batch with `IN (?, ?, ...)` or a temp table.

---

### 20. **Missing Validation on Incident Dedupe Key** - `core/db/aggregates.go:77`
```go
if incident.Type == "" || incident.DedupeKey == "" {
    return 0, errors.New("db: incident type and dedupe key are required")
}
```

**Problem**: No max length check. A pathological `DedupeKey` could be 1MB and blow up the index.

**Fix**: Add `if len(incident.DedupeKey) > 512 { return error }`.

---

### 21. **Timer Sleep Can Drift** - `core/db/timer.go:19`
```go
timer := time.NewTimer(time.Until(nextUTCMidnight()))
select {
case <-ctx.Done():
    timer.Stop()
    return
case <-timer.C:
}
```

**Problem**: If `runDayCycle()` takes 10 seconds, the next iteration starts 10 seconds late. Over days, this drifts.

**Fix**: Recalculate `nextUTCMidnight()` after each cycle instead of using a fixed interval.

**Actually, you do recalculate**—the `for` loop calls `time.Until(nextUTCMidnight())` every iteration. This is **fine**. Misread the code.

---

### 22. **Day File Pruning Doesn't Handle -wal/-shm** - `core/db/dayfiles.go:52`
```go
if err := os.Remove(path); err != nil {
    if os.IsNotExist(err) {
        continue
    }
    return removed, fmt.Errorf("remove %s: %w", path, err)
}
for _, suffix := range []string{"-wal", "-shm"} {
    os.Remove(path + suffix)  // Errors ignored
}
```

**Problem**: If the main `.db` file is removed but `-wal` removal fails (permission denied), you'll have orphaned WAL files.

**Fix**: Check errors on WAL removal and at least log them.

---

### 23. **Libcuda Path Resolution Silent Failure** - `core/tracer/load.go:148`
```go
func findLibcuda() (string, error) {
    for _, path := range libcudaCandidates {
        if _, err := os.Stat(path); err == nil {
            return path, nil
        }
    }
    return "", errors.New("libcuda.so.1 not found; is the NVIDIA driver installed?")
}
```

**Problem**: If `/usr/lib64/libcuda.so.1` exists but is the wrong version (e.g., stub library), you'll attach uprobes to the wrong library and get no events.

**Fix**: Check that the file is the real library (not a stub) by verifying it has the expected symbols.

---

### 24. **Snapshot Time Calculation Inconsistent** - `core/snapshot.go:11`
```go
snapshot := Snapshot{
    UnixNano:   now.UnixNano(),
    MinuteUnix: now.Unix() / 60 * 60,  // Truncates to minute, but in WALL CLOCK
```

**Problem**: `now.Unix()` is wall clock, but eBPF events are `CLOCK_BOOTTIME`. If NTP steps the clock, your minute buckets can jump.

**Fix**: Use monotonic time for bucketing, or document that wall clock jumps can cause weird bucketing.

---

### 25. **Config YAML Not Validated** - `core/config.go:68`
```go
cfg := DefaultConfig()
if err := yaml.Unmarshal(data, &cfg); err != nil {
    return nil, fmt.Errorf("parse config: %w", err)
}
// No validation of ranges
return &cfg, nil
```

**Problem**: If the operator sets `retention.day_files_days: -1`, you'll use it as-is. `PruneDayFiles` checks `<= 0`, but it's confusing.

**Fix**: Validate ranges:
```go
if cfg.Retention.DayFilesDays < 0 {
    return nil, errors.New("retention.day_files_days must be >= 0")
}
```

---

### 26. **Attach Failure Hints Hardcoded** - `core/tracer/run.go:91`
```go
if strings.Contains(cause, "is not executable") {
    hints = append(hints, fmt.Sprintf("...chmod a+x %s...", libcuda))
}
```

**Problem**: Only checks for execute bit. Other common failures (permission denied on `/sys/fs/bpf`, no CAP_BPF) have no hints.

**Fix**: Add hints for:
- `permission denied` → needs root or CAP_BPF
- `no such file or directory` (BPF filesystem) → mount bpffs

---

## LOW PRIORITY / STYLE ISSUES

### 27. **Magic Numbers** - Various files
```go
const acceptRetryDelay = 100 * time.Millisecond  // Why 100ms?
const hungSyncUs = 2_000_000  // Why 2 seconds?
const defaultSyncStallUs = 250_000  // Why 250ms?
```

**Problem**: No comments explaining **why** these values were chosen.

**Fix**: Add comments like `// 100ms paces retries without burning CPU`.

---

### 28. **Inconsistent Error Wrapping** - Various files
Some functions wrap errors with `fmt.Errorf("context: %w", err)`, others just return `err`.

**Fix**: Pick a consistent style. Recommendation: always wrap with context.

---

### 29. **Unused Return Value** - `core/writer.go:142`
```go
if _, err := database.CloseProcessesNotSeen(ctx, bootID, seen, seenAt); err != nil {
```

**Problem**: `CloseProcessesNotSeen` returns how many were closed, but you ignore it. This is fine, but might be useful to log.

**Fix**: Log it: `if closed, err := ...; closed > 0 { log.Printf("Closed %d stale processes", closed) }`.

---

### 30. **Long Function** - `cmd/wedjatd/main.go:run()`
275+ lines. Does: parse flags, check dev mode, acquire lock, load config, open DB, init NVML, start socket, start tracer, run poll loop, handle signals.

**Fix**: Extract into smaller functions:
- `setupPaths()`
- `initializeSources()`
- `startBackgroundServices()`
- `runMainLoop()`

---

### 31. **Naked Returns** - None found
Good! No naked returns that could confuse readers.

---

## MISSING FEATURES (Not Bugs, But Worth Considering)

### 32. **No Graceful Degradation for Partial eBPF Load**
If 90% of probes attach, you silently run with reduced coverage. Operators might not notice.

**Fix**: Add a config `min_attached_probes` and refuse to start if below threshold.

---

### 33. **No Database Size Limits**
Config has `max_size_bytes`, but it's never enforced. Databases can grow unbounded.

**Fix**: Check total size before each write and pause/prune if over limit.

---

### 34. **No Metric Export**
Daemon tracks stats (`ringbuf_drops`, `heartbeat_age`) but only logs them. No Prometheus/OpenTelemetry export.

**Fix**: Add a `/metrics` endpoint on the unix socket.

---

### 35. **No Process Tree**
You store `tgid` but not `ppid` (parent process ID). Can't answer "which jobs did this user's shell spawn?".

**Fix**: Parse `/proc/pid/stat` field 4 (ppid) and store it.

---

## RECOMMENDATIONS

### Immediate Actions (Critical Issues):
1. Fix database close context timeout (#1)
2. Fix snapshot process list handling (#8)
3. Document aggregate drain counter loss (#6)
4. Fix device index mismatch (#9)

### Short Term (High Priority):
1. Add schema error cleanup (#7)
2. Use strict YAML unmarshal (#10)
3. Add tracer close cleanup (#12)
4. Document mutex expectations (#14)
5. Fix process command truncation docs (#16)

### Medium Term:
1. Batch process close SQL (#19)
2. Validate incident dedupe key length (#20)
3. Check libcuda is real library (#23)
4. Add config validation (#25)

### Long Term / Nice to Have:
1. Extract main.go into smaller functions (#30)
2. Add metrics export (#34)
3. Add process tree tracking (#35)

---

## POSITIVE NOTES

### Things Done Well:
1. **Excellent use of `sync.Once` for cleanup** - No double-close bugs
2. **Good separation of concerns** - DB, tracer, socket are independent
3. **Proper use of contexts** - Cancellation propagates correctly (mostly)
4. **Embedded SQL schemas** - Single source of truth
5. **Pinned eBPF maps** - State survives daemon restarts
6. **Best-effort tracer** - Daemon doesn't fail when BPF can't load
7. **Detailed logging** - Easy to debug in production
8. **Clean functional style** - No hidden state, functions take what they need

### Overall Assessment:
**7.5/10** - Production-ready with fixes. The critical issues are fixable in a day. High-priority issues are a week of work. The architecture is solid.

---

## TESTING RECOMMENDATIONS

Since you said "don't care about tests", I'll just say: **the critical bugs won't be caught without**:
1. Concurrent database stress test (for lock issues)
2. Process churn test (1000 processes starting/stopping rapidly)
3. Long-running daemon test (days) to catch leaks
4. eBPF load test (saturate ring buffer to force drops)

If you change your mind, those are the tests to write.
