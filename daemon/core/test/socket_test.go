package core_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
)

func TestSocketBroadcastsSnapshotWithRestrictedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.sock")
	want := core.Snapshot{UnixNano: 42, ProcessesComplete: true}

	server, err := core.StartSocket(path, func() core.Snapshot { return want })
	if err != nil {
		t.Fatalf("start socket: %v", err)
	}
	defer server.Stop()

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
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var got core.Snapshot
	if err := json.NewDecoder(conn).Decode(&got); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if got.UnixNano != want.UnixNano || !got.ProcessesComplete {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSocketStopRemovesSocketFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.sock")
	server, err := core.StartSocket(path, func() core.Snapshot { return core.Snapshot{} })
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

func TestSocketUnknownGroupFailsAndLeavesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.sock")
	_, err := core.StartSocketWithOptions(path,
		func() core.Snapshot { return core.Snapshot{} },
		core.SocketOptions{Group: "no-such-group-wedjat"})
	if err == nil {
		t.Fatal("expected an error for an unknown group")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("socket file left behind after failed start: %v", statErr)
	}
}
