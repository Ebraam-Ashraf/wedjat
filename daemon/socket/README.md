# `daemon/socket` — Live Telemetry Feed

The Unix domain socket that serves the **in-progress minute** to connected
`wedjat` clients.

This is a required consumer, not a debugging extra. See
[Data Flow](../../docs/arch/data_flow.md) for the full architecture and
[Daemon Architecture](../../docs/arch/daemon.md) for how it fits the stage list.

---

## 1. Why this exists

The daemon aggregates on a 60-second flush interval. Between flushes, live state
exists **only in the collector's in-memory accumulator**, and it is destroyed at
each flush.

| Question | Source |
| :--- | :--- |
| "What did my job do at 03:00 last night?" | SQLite (`meta.db`, `YYYY-MM-DD.db`) |
| "What is the GPU doing right now?" | **This socket** |

The newest database row is up to 60 seconds stale and interval-averaged. A live
"current GPU state" tile **cannot** be served from disk. That is the entire
reason this component exists.

The daemon is the **server**. Clients are **read-only**. Nothing a client does
reaches the database, and the daemon records history whether or not any client
is connected — the socket is never on the write path.

---

## 2. Paths

| Mode | Path |
| :--- | :--- |
| System | `/run/wedjat/wedjat.sock` |
| `--dev` | `/tmp/wedjat-dev.sock` |

`PathForMode(dev bool)` returns the right one. In system mode the parent
directory is created by systemd via `RuntimeDirectory=wedjat` (mode `0770`), and
removed on stop, so the socket never outlives a stale process. Dev mode uses
`/tmp` so a development daemon cannot steal the production socket.

The socket file is created with mode **`0660`** — same telemetry as the
database, readable only by the daemon's group. See `scripts/wedjatd.service`
(`Group=wedjat`, `UMask=0027`).

---

## 3. Wire protocol

Newline-delimited JSON. One snapshot object per line, encoded **once per tick**
and written to every client, so all clients observe the same instant.

```json
{
  "unix_nano": 1700000000000000000,
  "minute_unix": 1700000000,
  "gpus": [
    {
      "index": 0,
      "util_gpu": 42,
      "util_mem": 18,
      "temp_c": 61,
      "power_mw": 275000,
      "vram_used": 4294967296,
      "sm_clock_mhz": 1410,
      "mem_clock_mhz": 7001,
      "valid": true
    }
  ],
  "processes": [
    { "pid": 4242, "gpu_index": 0, "vram_bytes": 67108864, "vram_valid": true }
  ]
}
```

- `minute_unix` matches the UTC minute bucketing used by the daily database, so a
  client can correlate a live frame with the row that will eventually hold it.
- `valid` distinguishes "idle at zero" from "not sampled yet". A field the
  collector has not observed is zero, and `valid: false` says so.
- The format is versioned by the Go struct, not by a schema version. Adding a
  field is backwards compatible; removing or retyping one is not.

---

## 4. Connecting

### From Go (the `wedjat` CLI)

Use the exported decoder so both sides share one definition of the format:

```go
conn, err := net.Dial("unix", socket.DefaultPath)
if err != nil {
	return err
}
defer conn.Close()

reader := bufio.NewReader(conn)
for {
	snapshot, err := socket.ReadSnapshot(reader)
	if err != nil {
		return err // io.EOF when the daemon stops
	}
	render(snapshot)
}
```

### From the shell

```bash
# Follow the live feed
socat - UNIX-CONNECT:/run/wedjat/wedjat.sock

# Or with netcat
nc -U /run/wedjat/wedjat.sock

# Pretty-print one frame
socat - UNIX-CONNECT:/run/wedjat/wedjat.sock | head -1 | jq .
```

### From Python

```python
import socket, json
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect("/run/wedjat/wedjat.sock")
buf = b""
while chunk := s.recv(65536):
    buf += chunk
    while b"\n" in buf:
        line, buf = buf.split(b"\n", 1)
        print(json.loads(line))
```

Read until you see `\n` — a frame may arrive split across several `recv`
calls, or several frames may arrive in one.

---

## 5. Limits

| Limit | Value | Enforced by |
| :--- | :--- | :--- |
| Pending connections (backlog) | 16 | `backlog` const, kernel accept queue |
| **Connected clients** | **unbounded** | *not currently limited* |
| Frame size | 1 MiB | `maxFrameBytes`, checked on encode and decode |
| Per-client write timeout | 5 s | `writeTimeout` |
| Broadcast interval | 2 s default | `Options.BroadcastInterval` |

### Known gap: no maximum client count

`backlog = 16` bounds how many connections the kernel queues before `accept`
runs. It does **not** cap how many clients stay connected — `Server.conns` is an
unbounded map. A client that connects and reads slowly is written to with a
5-second deadline and dropped on failure, so a stuck peer cannot wedge the
broadcast loop, but many healthy-but-idle peers each hold a file descriptor and
goroutine indefinitely.

This is fine for the expected audience (a handful of operators watching one
box). If you need to expose the socket more widely, add a `MaxClients` option
that refuses `accept` past a threshold and log the rejection. **Do not add it
speculatively** — the TUI is a single-user tool, and a cap that rejects a
legitimate viewer is worse than a few extra descriptors.

### Tuning the broadcast interval

`BroadcastInterval` should be **no faster than the collector's poll interval**
(`pollInterval = 2s` in `daemon/collector/collector.go`). A faster tick
resends identical data. A slower tick is legitimate if you want to reduce wakeups
on an idle box — the socket is the live view, not the durable record, so
dropping frames costs nothing that the database does not already hold.

---

## 6. Lifecycle

`Start` returns a `func(context.Context) error` matching
`bootstrap.StopFunc`, so it drops into the stage list the same way
`collector.Start` does.

**On start:** the server derives its own cancellable context, binds the socket,
and starts two goroutines — one accepting clients, one broadcasting on a ticker.

**On shutdown:** `Close` cancels the internal context, closes the listener,
closes every client connection, waits for the goroutines, and removes the socket
file. `Close` is idempotent.

> The server owns its context rather than relying on the caller's. This matters
> because `bootstrap.Runtime.Close` unwinds stages in reverse order and must
> complete even while a parent context is still live. An earlier version
> deadlocked here; the tests cover it.

**Failure handling:** a transient `accept` error is logged and retried, not
fatal. A client that fails to write is dropped. A client that disconnects
abnormally is reaped on the next write attempt.

**Startup safety:** if a *stale socket file* exists (crashed daemon), it is
reclaimed. If a **non-socket** file or a symlink exists at the path, `Start`
fails rather than unlinking it — the daemon must never destroy a file it does
not own.

---

## 7. Status

| Item | State |
| :--- | :--- |
| Server, protocol, lifecycle, limits | **Implemented** |
| 12 tests, passing under `-race -count=3` | **Implemented** |
| Registered as Stage 3 in `main.go` | **Wired** |
| `Source` reading the collector's accumulator | **Wired** via `collector.Handle.Live()` |
| `wedjat` CLI client | **Not written** |

The daemon now serves this socket. Stage 3 is registered in
`daemon/cmd/wedjatd/main.go` and reads the collector's in-memory sample through
`collector.Handle.Live()`, which publishes the raw NVML reading on every poll
tick. Because the accumulator is destroyed at each flush, that publish is what
makes the in-progress minute observable at all.

Verify a running daemon with:

```bash
socat - UNIX-CONNECT:/run/wedjat/wedjat.sock | head -1 | jq .
```

The CLI remains the only missing consumer.

---

## 8. Tests

```bash
cd daemon && go test ./socket/ -race
```

Covers: live broadcast, all clients seeing the same frame, history surviving
shutdown, idempotent close, dropped clients not blocking others, refusal to
replace a non-socket file, stale socket reclamation, `0660` permissions,
validation of missing source and relative paths, dev/system path isolation, and
oversized frame rejection.
