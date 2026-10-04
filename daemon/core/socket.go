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

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

// acceptRetryDelay paces the accept loop after a transient error so a
// persistent failure cannot spin the CPU.
const acceptRetryDelay = 100 * time.Millisecond

// writeDeadline is the maximum time a single write may take before the client
// is considered gone and disconnected.
const writeDeadline = 5 * time.Second

// perClientBufSize is the number of pre-encoded messages that can be queued
// per client. A slow client loses messages before it stalls others.
const perClientBufSize = 8

// wireMsg is the JSON envelope sent to every connected client.
type wireMsg struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp_unix_nano"`
	Data      json.RawMessage `json:"data"`
}

// encodeMsg serialises typeName + tsNano + data into a single JSON line.
func encodeMsg(typeName string, tsNano int64, data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireMsg{
		Type:      typeName,
		Timestamp: tsNano,
		Data:      raw,
	})
}

// SocketOptions configures the listening socket.
type SocketOptions struct {
	// Group, when non-empty, is the UNIX group that owns the socket file.
	// The socket is chmod 0660, so only the daemon's uid and members of this
	// group can connect.
	//
	// If the group cannot be resolved a warning is logged and the socket
	// continues without group restriction rather than aborting the daemon.
	Group string
}

// SocketServer broadcasts live GPU telemetry to connected clients.
type SocketServer struct {
	path     string
	sock     source.Chans
	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	mu      sync.Mutex
	clients map[net.Conn]chan []byte
}

// StartSocket creates and starts a unix socket server using the default options.
func StartSocket(path string, sock source.Chans) (*SocketServer, error) {
	return StartSocketWithOptions(path, sock, SocketOptions{})
}

// StartSocketWithOptions is StartSocket with explicit ownership settings.
func StartSocketWithOptions(path string, sock source.Chans, options SocketOptions) (*SocketServer, error) {
	// Remove a stale socket file from a previous run.
	os.Remove(path)

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
			log.Printf("socket: group %q could not be resolved (%v); "+
				"socket will be accessible only by the daemon's uid. "+
				"Ensure the group exists for a production install.", options.Group, err)
		} else if err := os.Chown(path, -1, gid); err != nil {
			log.Printf("socket: could not chown socket to group %q (%v); "+
				"socket will be accessible only by the daemon's uid.", options.Group, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := &SocketServer{
		path:     path,
		sock:     sock,
		listener: listener,
		ctx:      ctx,
		cancel:   cancel,
		clients:  make(map[net.Conn]chan []byte),
	}

	server.wg.Add(2)
	go server.acceptLoop()
	go server.broadcastLoop()

	return server, nil
}

// lookupGroup resolves a group name to its numeric GID.
func lookupGroup(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
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
			log.Printf("socket: accept error: %v", err)
			time.Sleep(acceptRetryDelay)
			continue
		}

		// Per-client buffered channel; sized so a slow client loses messages
		// before it can block the broadcaster.
		clientCh := make(chan []byte, perClientBufSize)

		s.mu.Lock()
		s.clients[conn] = clientCh
		s.mu.Unlock()

		source.Clients.Add(1)

		s.wg.Add(2)
		go s.readClient(conn)
		go s.writeClient(conn, clientCh)
	}
}

// readClient blocks on a single Read to detect client disconnection.
// It does not loop; one read is enough to notice an EOF or error.
func (s *SocketServer) readClient(conn net.Conn) {
	defer s.wg.Done()
	defer s.removeClient(conn)

	buf := make([]byte, 4096)
	conn.Read(buf) // blocks until disconnect or error
}

// writeClient drains the per-client channel and writes each encoded message
// to the connection. It exits when the channel is closed or on a write error.
func (s *SocketServer) writeClient(conn net.Conn, ch chan []byte) {
	defer s.wg.Done()
	defer s.removeClient(conn)

	for {
		select {
		case <-s.ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil {
				return
			}
			if _, err := conn.Write(msg); err != nil {
				return
			}
		}
	}
}

// removeClient closes and deregisters a client connection.
func (s *SocketServer) removeClient(conn net.Conn) {
	s.mu.Lock()
	_, exists := s.clients[conn]
	if exists {
		delete(s.clients, conn)
	}
	s.mu.Unlock()

	if exists {
		source.Clients.Add(-1)
		conn.Close()
	}
}

// broadcastLoop selects over all five source channels, encodes each message
// once, and offers it to every connected client's per-client channel.
// Slow clients lose messages; they never stall others.
func (s *SocketServer) broadcastLoop() {
	defer s.wg.Done()

	for {
		var msg []byte
		var err error

		select {
		case <-s.ctx.Done():
			return

		case gpus, ok := <-s.sock.GPU:
			if !ok {
				return
			}
			if len(gpus) == 0 {
				continue
			}
			ts := gpus[0].TsNano
			msg, err = encodeMsg("gpu", ts, gpus)

		case pl, ok := <-s.sock.Procs:
			if !ok {
				return
			}
			msg, err = encodeMsg("procs", pl.TsNano, pl)

		case xid, ok := <-s.sock.Xid:
			if !ok {
				return
			}
			msg, err = encodeMsg("xid", xid.TsNano, xid)

		case agg, ok := <-s.sock.Agg:
			if !ok {
				return
			}
			if len(agg) == 0 {
				continue
			}
			ts := agg[0].TsNano
			msg, err = encodeMsg("agg", ts, agg)

		case ev, ok := <-s.sock.Event:
			if !ok {
				return
			}
			msg, err = encodeMsg("event", int64(ev.TsNano), ev)
		}

		if err != nil {
			log.Printf("socket: encode message: %v", err)
			continue
		}

		// Append newline so readers can use bufio.Scanner.
		msg = append(msg, '\n')

		s.mu.Lock()
		for _, ch := range s.clients {
			select {
			case ch <- msg:
			default:
				// Client is too slow; drop this message for them.
			}
		}
		s.mu.Unlock()
	}
}

// Stop shuts down the socket server and waits for all goroutines to exit.
func (s *SocketServer) Stop() error {
	s.cancel()
	s.listener.Close()

	s.mu.Lock()
	for conn, ch := range s.clients {
		close(ch)
		conn.Close()
		delete(s.clients, conn)
	}
	s.mu.Unlock()

	s.wg.Wait()
	os.Remove(s.path)
	return nil
}
