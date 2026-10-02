package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/user"
	"strconv"
	"sync"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/nvml"
)

// acceptRetryDelay paces the accept loop after an error so a persistent
// failure cannot spin the CPU.
const acceptRetryDelay = 100 * time.Millisecond

// Snapshot represents the current live state of GPUs and processes.
type Snapshot struct {
	UnixNano   int64                `json:"unix_nano"`
	MinuteUnix int64                `json:"minute_unix"`
	GPUs       []nvml.GPUSample     `json:"gpus"`
	Processes  []nvml.ProcessSample `json:"processes"`
	// ProcessesComplete is false when at least one device failed to report its
	// process list. Consumers must not treat the process list as the full set
	// of running GPU processes when it is false.
	ProcessesComplete bool `json:"processes_complete"`
}

// SnapshotFunc is a function that returns the current snapshot.
type SnapshotFunc func() Snapshot

// SocketServer broadcasts live telemetry to connected clients.
type SocketServer struct {
	path        string
	getSnapshot SnapshotFunc
	listener    net.Listener
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// SocketOptions configures the listening socket.
type SocketOptions struct {
	// Group, when set, is the group that owns the socket. Telemetry names
	// processes and their GPU memory, so the socket must not be readable by
	// every local user.
	Group string
}

// StartSocket creates and starts a unix socket server that broadcasts snapshots.
func StartSocket(path string, getSnapshot SnapshotFunc) (*SocketServer, error) {
	return StartSocketWithOptions(path, getSnapshot, SocketOptions{})
}

// StartSocketWithOptions is StartSocket with explicit ownership settings.
func StartSocketWithOptions(path string, getSnapshot SnapshotFunc, options SocketOptions) (*SocketServer, error) {
	// Remove stale socket file if it exists
	os.Remove(path)

	// Listen on unix socket
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on socket: %w", err)
	}

	// Restrict the socket to its owner and group. Anything looser exposes
	// every process name and VRAM figure the daemon knows to any local user.
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	if options.Group != "" {
		gid, err := lookupGroup(options.Group)
		if err != nil {
			listener.Close()
			os.Remove(path)
			return nil, fmt.Errorf("resolve socket group %q: %w", options.Group, err)
		}
		if err := os.Chown(path, -1, gid); err != nil {
			listener.Close()
			os.Remove(path)
			return nil, fmt.Errorf("chown socket: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := &SocketServer{
		path:        path,
		getSnapshot: getSnapshot,
		listener:    listener,
		ctx:         ctx,
		cancel:      cancel,
	}

	// Start accept loop
	server.wg.Add(1)
	go server.acceptLoop()

	return server, nil
}

// lookupGroup resolves a group name to its numeric id.
func lookupGroup(name string) (int, error) {
	gid, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(gid.Gid)
}

// acceptLoop accepts new client connections.
func (s *SocketServer) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
			}
			// Without a pause a persistent error, such as a descriptor
			// shortage, turns this into a busy loop that burns a core.
			log.Printf("socket: accept error: %v", err)
			time.Sleep(acceptRetryDelay)
			continue
		}

		// Handle client in separate goroutine
		s.wg.Add(1)
		go s.handleClient(conn)
	}
}

// handleClient broadcasts snapshots to a single client.
func (s *SocketServer) handleClient(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	encoder := json.NewEncoder(conn)

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-ticker.C:
			snapshot := s.getSnapshot()

			// Set write deadline to avoid blocking on slow clients
			if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return
			}

			if err := encoder.Encode(snapshot); err != nil {
				// Client disconnected or too slow
				return
			}
		}
	}
}

// Stop stops the socket server and closes all connections.
func (s *SocketServer) Stop() error {
	s.cancel()
	s.listener.Close()
	s.wg.Wait()
	os.Remove(s.path)
	return nil
}
