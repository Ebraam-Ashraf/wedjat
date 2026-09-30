package socket_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/Ebraam-Ashraf/wedjat/daemon/socket"
)

func testPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "wedjat.sock")
}

func fixedSnapshot() Snapshot {
	return Snapshot{
		UnixNano:   1_700_000_000_000_000_000,
		MinuteUnix: 1_700_000_000,
		GPUs: []GPUSnapshot{{
			Index: 0, UtilGPU: 42, TempC: 61, PowerMW: 275_000,
			VRAMUsed: 4_294_967_296, SMClockMHz: 1410, Valid: true,
		}},
		Processes: []ProcessSnapshot{{PID: 4242, GPUIndex: 0, VRAMBytes: 67_108_864, VRAMValid: true}},
	}
}

func dial(t *testing.T, path string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return conn
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("dial %s: no listener appeared within the deadline", path)
	return nil
}

func awaitSnapshot(t *testing.T, conn net.Conn) Snapshot {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	snapshot, err := ReadSnapshot(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	return snapshot
}

func TestBroadcastsLiveSnapshotToConnectedClient(t *testing.T) {
	path := testPath(t)
	stop, err := Start(context.Background(), Options{
		Path:              path,
		Source:            func() Snapshot { return fixedSnapshot() },
		BroadcastInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())

	conn := dial(t, path)
	defer conn.Close()

	snapshot := awaitSnapshot(t, conn)
	if snapshot.MinuteUnix != 1_700_000_000 {
		t.Fatalf("minute = %d", snapshot.MinuteUnix)
	}
	if len(snapshot.GPUs) != 1 || snapshot.GPUs[0].UtilGPU != 42 {
		t.Fatalf("gpu payload = %+v", snapshot.GPUs)
	}
	if len(snapshot.Processes) != 1 || snapshot.Processes[0].PID != 4242 {
		t.Fatalf("process payload = %+v", snapshot.Processes)
	}
}

func TestAllClientsReceiveTheSameFrame(t *testing.T) {
	path := testPath(t)
	var mu sync.Mutex
	calls := 0
	stop, err := Start(context.Background(), Options{
		Path: path,
		Source: func() Snapshot {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return fixedSnapshot()
		},
		BroadcastInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())

	first := dial(t, path)
	defer first.Close()
	second := dial(t, path)
	defer second.Close()

	a := awaitSnapshot(t, first)
	b := awaitSnapshot(t, second)
	if a.UnixNano != b.UnixNano || a.MinuteUnix != b.MinuteUnix {
		t.Fatalf("clients saw different frames: %+v vs %+v", a, b)
	}
}

func TestHistorySurvivesDaemonShutdown(t *testing.T) {
	// The socket is the live feed only. Stopping the server must not disturb
	// the database, because history is the durable record the CLI falls back
	// to when the daemon is not running.
	path := testPath(t)
	stop, err := Start(context.Background(), Options{
		Path:              path,
		Source:            func() Snapshot { return fixedSnapshot() },
		BroadcastInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := dial(t, path)
	awaitSnapshot(t, conn)
	conn.Close()

	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file survived shutdown: %v", err)
	}
}

func TestCloseIsIdempotentAndRemovesSocketFile(t *testing.T) {
	path := testPath(t)
	stop, err := Start(context.Background(), Options{
		Path:              path,
		Source:            func() Snapshot { return fixedSnapshot() },
		BroadcastInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	dial(t, path).Close()

	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("second stop returned %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file still present: %v", err)
	}
}

func TestDisconnectedClientIsDroppedAndDoesNotBlockOthers(t *testing.T) {
	path := testPath(t)
	stop, err := Start(context.Background(), Options{
		Path:              path,
		Source:            func() Snapshot { return fixedSnapshot() },
		BroadcastInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())

	stale := dial(t, path)
	stale.Close()
	survivor := dial(t, path)
	defer survivor.Close()

	// The survivor must keep receiving frames after the stale client vanishes.
	for i := 0; i < 3; i++ {
		if snapshot := awaitSnapshot(t, survivor); len(snapshot.GPUs) != 1 {
			t.Fatalf("survivor frame %d malformed: %+v", i, snapshot)
		}
	}
}

func TestRefusesToReplaceNonSocketFile(t *testing.T) {
	// Unlinking a regular file the daemon does not own would destroy data.
	path := testPath(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Options{
		Path:   path,
		Source: func() Snapshot { return fixedSnapshot() },
	})
	if err == nil {
		t.Fatal("expected refusal to replace a regular file")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "not a socket" {
		t.Fatalf("existing file was clobbered: %q %v", data, readErr)
	}
}

func TestReplacesStaleSocketFile(t *testing.T) {
	// A crashed daemon leaves the socket file behind. The next start must
	// reclaim it rather than refuse to boot.
	path := testPath(t)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.Close() // simulates a crash: file remains, nothing is listening

	stop, err := Start(context.Background(), Options{
		Path:              path,
		Source:            func() Snapshot { return fixedSnapshot() },
		BroadcastInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("stale socket was not reclaimed: %v", err)
	}
	defer stop(context.Background())
	awaitSnapshot(t, dial(t, path))
}

func TestSocketPermissionsMatchDaemonGroup(t *testing.T) {
	path := testPath(t)
	stop, err := Start(context.Background(), Options{
		Path:              path,
		Source:            func() Snapshot { return fixedSnapshot() },
		BroadcastInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0660 {
		t.Fatalf("socket mode = %o, want 660", perm)
	}
}

func TestRejectsMissingSource(t *testing.T) {
	if _, err := Start(context.Background(), Options{Path: testPath(t)}); err == nil {
		t.Fatal("expected a missing source to be rejected")
	}
}

func TestRejectsRelativePath(t *testing.T) {
	_, err := Start(context.Background(), Options{
		Path:   "relative.sock",
		Source: func() Snapshot { return fixedSnapshot() },
	})
	if err == nil {
		t.Fatal("expected a relative path to be rejected")
	}
}

func TestPathForModeIsolatesDevSocket(t *testing.T) {
	system := PathForMode(false)
	dev := PathForMode(true)
	if system != DefaultPath {
		t.Fatalf("system path = %s", system)
	}
	if dev == system {
		t.Fatal("dev mode must not share the system socket path")
	}
	if !filepath.IsAbs(dev) {
		t.Fatalf("dev path must be absolute: %s", dev)
	}
}

func TestReadSnapshotRejectsOversizedFrame(t *testing.T) {
	// A frame larger than the server's write limit must be rejected rather
	// than buffered without bound.
	reader := bufio.NewReader(newLineReader(make([]byte, 2<<20)))
	if _, err := ReadSnapshot(reader); err == nil {
		t.Fatal("expected an oversized frame to be rejected")
	}
}
