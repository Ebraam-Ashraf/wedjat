package core_test

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// makeTestChans creates a source.Chans pair with small buffers for testing.
func makeTestChans() (source.Chans, source.Chans) {
	db := source.NewChans(16)
	sc := source.NewChans(16)
	return db, sc
}

func skipWhenUnixSocketsAreDenied(t *testing.T) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe.sock")
	listener, err := net.Listen("unix", probe)
	if err == nil {
		_ = listener.Close()
		return
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("sandbox does not permit Unix sockets: %v", err)
	}
	t.Fatalf("probe Unix socket: %v", err)
}

func TestSocketBroadcastsGPUSample(t *testing.T) {
	skipWhenUnixSocketsAreDenied(t)
	path := filepath.Join(t.TempDir(), "w.sock")
	_, sc := makeTestChans()

	server, err := core.StartSocket(path, sc)
	if err != nil {
		t.Fatalf("start socket: %v", err)
	}
	defer server.Stop()

	// Check permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0660 {
		t.Fatalf("socket mode = %o, want 660", perm)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Wait for source.Clients to reflect the connection.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if source.Clients.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	want := []source.GPUSample{{
		TsNano: 42_000_000_000,
		UUID:   "GPU-test",
		Valid:  true,
	}}
	source.Send(sc.GPU, want)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(conn).Decode(&raw); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	var msgType string
	if err := json.Unmarshal(raw["type"], &msgType); err != nil {
		t.Fatalf("decode type: %v", err)
	}
	if msgType != "gpu" {
		t.Fatalf("type = %q, want \"gpu\"", msgType)
	}
}

func TestSocketBroadcastsToEveryClient(t *testing.T) {
	skipWhenUnixSocketsAreDenied(t)
	path := filepath.Join(t.TempDir(), "w.sock")
	_, sc := makeTestChans()

	server, err := core.StartSocket(path, sc)
	if err != nil {
		t.Fatalf("start socket: %v", err)
	}
	defer server.Stop()

	clients := make([]net.Conn, 2)
	var err2 error
	for i := range clients {
		clients[i], err2 = net.Dial("unix", path)
		if err2 != nil {
			t.Fatalf("dial client %d: %v", i, err2)
		}
		defer clients[i].Close()
		clients[i].SetReadDeadline(time.Now().Add(5 * time.Second))
	}

	// Wait for both clients to register.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if source.Clients.Load() == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	gpus := []source.GPUSample{{TsNano: 84_000_000_000, UUID: "GPU-two"}}
	source.Send(sc.GPU, gpus)

	for i, conn := range clients {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(conn).Decode(&raw); err != nil {
			t.Fatalf("decode for client %d: %v", i, err)
		}
		var msgType string
		if err := json.Unmarshal(raw["type"], &msgType); err != nil {
			t.Fatalf("client %d decode type: %v", i, err)
		}
		if msgType != "gpu" {
			t.Fatalf("client %d: type = %q, want \"gpu\"", i, msgType)
		}
	}
}

func TestSocketStopRemovesSocketFile(t *testing.T) {
	skipWhenUnixSocketsAreDenied(t)
	path := filepath.Join(t.TempDir(), "w.sock")
	_, sc := makeTestChans()

	server, err := core.StartSocket(path, sc)
	if err != nil {
		t.Fatalf("start socket: %v", err)
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket file still present after Stop: %v", err)
	}
}

func TestSocketUnknownGroupLogsWarningButContinues(t *testing.T) {
	skipWhenUnixSocketsAreDenied(t)
	path := filepath.Join(t.TempDir(), "w.sock")
	_, sc := makeTestChans()

	server, err := core.StartSocketWithOptions(
		path,
		sc,
		core.SocketOptions{Group: "no-such-group-wedjat"},
	)
	if err != nil {
		t.Fatalf("StartSocketWithOptions with unknown group should not fail: %v", err)
	}
	defer server.Stop()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file missing after start with unknown group: %v", err)
	}
}

func TestSocketClientCountTracking(t *testing.T) {
	skipWhenUnixSocketsAreDenied(t)
	source.Clients.Store(0) // reset in case previous tests left a count

	path := filepath.Join(t.TempDir(), "w.sock")
	_, sc := makeTestChans()

	server, err := core.StartSocket(path, sc)
	if err != nil {
		t.Fatalf("start socket: %v", err)
	}
	defer server.Stop()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Wait for the client count to increment.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if source.Clients.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if source.Clients.Load() != 1 {
		t.Fatalf("source.Clients = %d, want 1 after connect", source.Clients.Load())
	}

	conn.Close()

	// Wait for the client count to decrement.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if source.Clients.Load() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if source.Clients.Load() != 0 {
		t.Fatalf("source.Clients = %d, want 0 after disconnect", source.Clients.Load())
	}
}

func TestSocketWireFormat(t *testing.T) {
	skipWhenUnixSocketsAreDenied(t)
	path := filepath.Join(t.TempDir(), "w.sock")
	_, sc := makeTestChans()

	server, err := core.StartSocket(path, sc)
	if err != nil {
		t.Fatalf("start socket: %v", err)
	}
	defer server.Stop()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Wait for connection.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if source.Clients.Load() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	xid := source.Xid{TsNano: 999_000_000_000, UUID: "GPU-x", Code: 79}
	source.Send(sc.Xid, xid)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var msg struct {
		Type      string          `json:"type"`
		Timestamp int64           `json:"timestamp_unix_nano"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(conn).Decode(&msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != "xid" {
		t.Fatalf("type = %q, want \"xid\"", msg.Type)
	}
	if msg.Timestamp != 999_000_000_000 {
		t.Fatalf("timestamp = %d, want 999_000_000_000", msg.Timestamp)
	}
}
