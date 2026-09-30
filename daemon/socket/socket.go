// Package socket serves the live in-progress minute to connected wedjat
// clients over a Unix domain socket.
//
// The socket is a required consumer, not a debugging extra. It carries the one
// class of telemetry that exists nowhere else: the collector's in-memory
// accumulator, which is destroyed at every flush. The newest row in the daily
// database is up to one flush interval stale and interval-averaged, so a
// "current GPU state" view cannot be served from disk.
//
// The daemon is the server and clients are read-only. Nothing a client does
// reaches the database, and the daemon records history whether or not any
// client is connected.
package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DefaultPath is the system-mode socket path. The systemd unit creates
	// /run/wedjat via RuntimeDirectory=wedjat.
	DefaultPath = "/run/wedjat/wedjat.sock"

	// devPath keeps development sockets out of the system runtime directory,
	// mirroring how the daemon isolates its lock file under --dev.
	devPath = "/tmp/wedjat-dev.sock"

	// backlog bounds pending connections; clients are lightweight readers.
	backlog = 16

	// maxFrameBytes caps one encoded snapshot so a malformed or hostile
	// client cannot force unbounded buffering on the write path.
	maxFrameBytes = 1 << 20

	// writeTimeout bounds a single client write so one stalled reader cannot
	// wedge the broadcast loop for every other client.
	writeTimeout = 5 * time.Second
)

// Snapshot is the live state of the minute currently being accumulated.
//
// Every field is a pointer-free value so a snapshot is trivially copyable and
// safe to hand to a writer goroutine. Fields that the collector has not yet
// observed are zero, and Valid distinguishes "idle at zero" from "not sampled".
type Snapshot struct {
	// UnixNano is the collector's sample timestamp for this snapshot.
	UnixNano int64 `json:"unix_nano"`

	// MinuteUnix is the UTC minute this snapshot belongs to, matching the
	// ts bucketing used by the daily database.
	MinuteUnix int64 `json:"minute_unix"`

	// GPUs holds one entry per discovered GPU.
	GPUs []GPUSnapshot `json:"gpus"`

	// Processes holds one entry per (pid, gpu) pair seen this minute.
	Processes []ProcessSnapshot `json:"processes"`
}

// GPUSnapshot is the live board state for one GPU.
type GPUSnapshot struct {
	Index       int   `json:"index"`
	UtilGPU     int   `json:"util_gpu"`
	UtilMem     int   `json:"util_mem"`
	TempC       int   `json:"temp_c"`
	PowerMW     int   `json:"power_mw"`
	VRAMUsed    int64 `json:"vram_used"`
	SMClockMHz  int   `json:"sm_clock_mhz"`
	MemClockMHz int   `json:"mem_clock_mhz"`
	Valid       bool  `json:"valid"`
}

// ProcessSnapshot is the live per-process state for one GPU.
type ProcessSnapshot struct {
	PID       int   `json:"pid"`
	GPUIndex  int   `json:"gpu_index"`
	VRAMBytes int64 `json:"vram_bytes"`
	VRAMValid bool  `json:"vram_valid"`
}

// Source produces the current snapshot. It is called on the broadcast tick and
// must not block; the collector satisfies this by handing over an already-built
// copy of its accumulator.
type Source func() Snapshot

// Options configures a Server.
type Options struct {
	// Path is the socket path to bind.
	Path string

	// Source supplies the snapshot broadcast to clients. Required.
	Source Source

	// BroadcastInterval is how often each client receives a snapshot. It
	// should be no faster than the collector's poll interval; faster ticks
	// resend identical data.
	BroadcastInterval time.Duration

	// Clock supplies the current time. Tests override it.
	Clock func() time.Time
}

func (o *Options) withDefaults() error {
	if o.Path == "" {
		o.Path = DefaultPath
	}
	if o.Source == nil {
		return errors.New("socket: a snapshot source is required")
	}
	if o.BroadcastInterval <= 0 {
		o.BroadcastInterval = 2 * time.Second
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if !filepath.IsAbs(o.Path) || filepath.Clean(o.Path) != o.Path {
		return errors.New("socket: path must be a clean absolute path")
	}
	if filepath.Base(o.Path) == "." || o.Path == string(filepath.Separator) {
		return errors.New("socket: refusing to bind an invalid path")
	}
	return nil
}

// PathForMode returns the socket path for the given run mode.
func PathForMode(dev bool) string {
	if dev {
		return devPath
	}
	return DefaultPath
}

// Server broadcasts snapshots to connected clients.
type Server struct {
	opts  Options
	mu    sync.Mutex
	conns map[*client]struct{}
	// listener is closed by Close to unblock Accept.
	listener net.Listener
	// removePath is the socket file to unlink on shutdown.
	removePath string
	closed     bool
	// cancel stops the accept and broadcast loops. Close cancels it rather
	// than relying on the caller's context, so shutdown completes even when
	// the parent context is still live.
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type client struct {
	conn net.Conn
	// dropped is closed once the client is removed, so a blocked writer
	// unblocks on shutdown instead of holding the broadcast loop.
	dropped chan struct{}
	once    sync.Once
}

func (c *client) close() {
	c.once.Do(func() { close(c.dropped) })
}

// Start binds the socket and begins accepting clients. The returned function
// closes all connections and removes the socket file.
func Start(ctx context.Context, opts Options) (func(context.Context) error, error) {
	if ctx == nil {
		return nil, errors.New("socket: nil context")
	}
	if err := opts.withDefaults(); err != nil {
		return nil, err
	}

	// The loops are bound to a context this server owns, so Close terminates
	// them regardless of the caller's context lifetime.
	loopCtx, cancel := context.WithCancel(ctx)

	s := &Server{
		opts:       opts,
		conns:      make(map[*client]struct{}),
		removePath: opts.Path,
		cancel:     cancel,
	}

	listener, err := listen(opts.Path)
	if err != nil {
		cancel()
		return nil, err
	}
	s.listener = listener

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(loopCtx)
	}()
	go func() {
		defer s.wg.Done()
		s.broadcastLoop(loopCtx)
	}()

	log.Printf("[socket] Listening on %s (broadcast every %s)", opts.Path, opts.BroadcastInterval)

	stop := func(context.Context) error { return s.Close() }
	return stop, nil
}

// listen binds the Unix socket, refusing a pre-existing path that is not a
// socket we can safely replace. A stale socket file left by a crashed daemon is
// removed; anything else (a regular file, a symlink, a directory) is an error,
// because unlinking it could destroy data this daemon does not own.
func listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0770); err != nil {
		return nil, fmt.Errorf("socket: create runtime directory: %w", err)
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("socket: listen on %s: %w", path, err)
	}
	// The socket carries the same live telemetry as the database, so it is
	// readable only by the daemon's group (see wedjatd.service UMask=0027).
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, fmt.Errorf("socket: set permissions on %s: %w", path, err)
	}
	return listener, nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("socket: inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("socket: refusing to replace symlink at %s", path)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("socket: refusing to replace non-socket file at %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("socket: remove stale socket %s: %w", path, err)
	}
	return nil
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.isClosed() || ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			// A transient accept failure must not kill the server.
			log.Printf("[socket] Accept failed: %v", err)
			continue
		}
		c := &client{conn: conn, dropped: make(chan struct{})}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
	}
}

func (s *Server) broadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(s.opts.BroadcastInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.broadcast()
		}
	}
}

func (s *Server) broadcast() {
	s.mu.Lock()
	if len(s.conns) == 0 {
		s.mu.Unlock()
		return
	}
	targets := make([]*client, 0, len(s.conns))
	for c := range s.conns {
		targets = append(targets, c)
	}
	s.mu.Unlock()

	// Encode once, write to all: every client sees the same instant.
	snapshot := s.opts.Source()
	frame, err := encodeFrame(snapshot)
	if err != nil {
		log.Printf("[socket] Failed to encode snapshot: %v", err)
		return
	}

	for _, c := range targets {
		if err := c.write(frame); err != nil {
			log.Printf("[socket] Dropping client: %v", err)
			s.remove(c)
		}
	}
}

// write sends one frame to a client, honouring both the write timeout and the
// client's drop signal.
func (c *client) write(frame []byte) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	// A newline-delimited frame lets a client read line by line without
	// framing the stream itself.
	if _, err := c.conn.Write(append(frame, '\n')); err != nil {
		return err
	}
	return nil
}

func encodeFrame(snapshot Snapshot) ([]byte, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(data) > maxFrameBytes {
		return nil, fmt.Errorf("socket: snapshot of %d bytes exceeds limit", len(data))
	}
	return data, nil
}

func (s *Server) remove(c *client) {
	s.mu.Lock()
	_, ok := s.conns[c]
	delete(s.conns, c)
	s.mu.Unlock()
	if ok {
		c.close()
		c.conn.Close()
	}
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Close stops the server, disconnects every client, and removes the socket
// file. It is safe to call more than once.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]*client, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = make(map[*client]struct{})
	listener := s.listener
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if listener != nil {
		listener.Close()
	}
	for _, c := range conns {
		c.close()
		c.conn.Close()
	}
	s.wg.Wait()

	if s.removePath != "" {
		if err := os.Remove(s.removePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("socket: remove %s: %w", s.removePath, err)
		}
	}
	log.Printf("[socket] Shutdown complete.")
	return nil
}

// ReadSnapshot decodes one newline-delimited frame from a client connection.
// It is the client-side counterpart of the broadcast format and is exported so
// the wedjat CLI shares exactly one definition of the wire format.
func ReadSnapshot(r *bufio.Reader) (Snapshot, error) {
	line, err := readFrame(r)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(line, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("socket: decode snapshot: %w", err)
	}
	return snapshot, nil
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(frame) > 0 {
				break
			}
			return nil, err
		}
		frame = append(frame, chunk...)
		if len(frame) > maxFrameBytes {
			return nil, fmt.Errorf("socket: frame exceeds %d bytes", maxFrameBytes)
		}
		if !isPrefix {
			break
		}
	}
	if len(frame) == 0 {
		return nil, io.EOF
	}
	return frame, nil
}
