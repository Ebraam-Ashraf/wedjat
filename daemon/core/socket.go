package core

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/user"
	"strconv"
	"sync"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
	"github.com/Ebraam-Ashraf/wedjat/internal/wire"
)

// acceptRetryDelay paces the accept loop after a transient error so a
// persistent failure cannot spin the CPU.
const acceptRetryDelay = 100 * time.Millisecond

// writeDeadline is the maximum time a single write may take before the client
// is considered gone and disconnected.
const writeDeadline = 5 * time.Second

// perClientBufSize is the number of non-GPU messages that can be queued per
// client. GPU samples have their own larger queue so process/event traffic
// cannot evict graph samples.
const perClientBufSize = 32
const perClientGPUBufferSize = 128

type clientQueues struct {
	gpu   chan []byte
	other chan []byte
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
	clients map[net.Conn]*clientQueues
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
		clients:  make(map[net.Conn]*clientQueues),
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
		clientCh := &clientQueues{
			gpu:   make(chan []byte, perClientGPUBufferSize),
			other: make(chan []byte, perClientBufSize),
		}

		s.mu.Lock()
		s.clients[conn] = clientCh
		s.mu.Unlock()

		source.Clients.Add(1)
		log.Printf("Socket: new client connected (total: %d)", source.Clients.Load())

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
func (s *SocketServer) writeClient(conn net.Conn, queues *clientQueues) {
	defer s.wg.Done()
	defer s.removeClient(conn)

	for {
		// Prefer GPU samples so a burst of process/event messages cannot hold
		// the live charts behind a non-GPU backlog.
		select {
		case <-s.ctx.Done():
			return
		case msg, ok := <-queues.gpu:
			if !ok {
				return
			}
			if !s.writeMessage(conn, msg) {
				return
			}
			continue
		default:
		}

		select {
		case <-s.ctx.Done():
			return
		case msg, ok := <-queues.gpu:
			if !ok {
				return
			}
			if !s.writeMessage(conn, msg) {
				return
			}
		case msg, ok := <-queues.other:
			if !ok {
				return
			}
			if !s.writeMessage(conn, msg) {
				return
			}
		}
	}
}

func (s *SocketServer) writeMessage(conn net.Conn, msg []byte) bool {
	if err := conn.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil {
		return false
	}
	_, err := conn.Write(msg)
	return err == nil
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
		log.Printf("Socket: client disconnected (total: %d)", source.Clients.Load())
		conn.Close()
	}
}

// sendToClient queues one already-encoded message for a single client, never
// blocking the broadcaster.
//
// When the queue is full the OLDEST message is discarded rather than the new
// one. Live telemetry is only useful while it is current: dropping the newest
// would leave a briefly slow client permanently behind, because it would keep
// draining a backlog it can never catch up on, and its chart would never reach
// the right edge. Dropping the oldest lets such a client jump straight back to
// current, and a client that keeps up never sees a difference.
//
// Every discarded message is counted in source.Dropped, which the database
// heartbeat already reports, so client-side loss shows up in the log instead of
// being silently invisible.
func sendToClient(ch chan []byte, msg []byte) bool {
	dropped := false
	for attempt := 0; attempt < 2; attempt++ {
		select {
		case ch <- msg:
			return dropped
		default:
		}

		// Queue is full: make room by discarding the oldest message.
		select {
		case <-ch:
			source.Dropped.Add(1)
			dropped = true
		default:
			// The client's writer drained the queue between the two
			// selects, so nothing was lost. Retry the send.
		}
	}
	// Two attempts still could not land the message, so this client is not
	// draining its queue at all. Give up rather than stall every other
	// client; any message already discarded above is counted.
	return dropped
}

// toWireGPUSamples converts source GPUSample slice to wire GPUSample slice.
func toWireGPUSamples(src []source.GPUSample) []wire.GPUSample {
	dst := make([]wire.GPUSample, len(src))
	for i, s := range src {
		dst[i] = wire.GPUSample{
			TsNano:         s.TsNano,
			UUID:           s.UUID,
			Index:          s.Index,
			UtilGPU:        s.UtilGPU,
			UtilMem:        s.UtilMem,
			MemUsed:        s.MemUsed,
			TempC:          s.TempC,
			PowerMW:        s.PowerMW,
			PowerLimitMW:   s.PowerLimitMW,
			SMClockMHz:     s.SMClockMHz,
			MemClockMHz:    s.MemClockMHz,
			ThrottleReason: s.ThrottleReason,
			ECCErrors:      s.ECCErrors,
			ValidFields:    s.ValidFields,
			Valid:          s.Valid,
		}
	}
	return dst
}

// toWireProcList converts source ProcList to wire ProcList.
func toWireProcList(src source.ProcList) wire.ProcList {
	procs := make([]wire.ProcessSample, len(src.Procs))
	for i, p := range src.Procs {
		procs[i] = wire.ProcessSample{
			PID:       p.PID,
			GPUUUID:   p.GPUUUID,
			VRAMBytes: p.VRAMBytes,
			VRAMValid: p.VRAMValid,
		}
	}
	return wire.ProcList{
		TsNano:   src.TsNano,
		Procs:    procs,
		Complete: src.Complete,
	}
}

// toWireXid converts source Xid to wire Xid.
func toWireXid(src source.Xid) wire.Xid {
	return wire.Xid{
		TsNano: src.TsNano,
		UUID:   src.UUID,
		Index:  src.Index,
		Code:   src.Code,
	}
}

// toWireAggRows converts source AggRow slice to wire AggRow slice.
func toWireAggRows(src []source.AggRow) []wire.AggRow {
	dst := make([]wire.AggRow, len(src))
	for i, a := range src {
		dst[i] = wire.AggRow{
			TsNano:       a.TsNano,
			Tgid:         a.Tgid,
			Ordinal:      a.Ordinal,
			ApiID:        a.ApiID,
			Count:        a.Count,
			Bytes:        a.Bytes,
			LatencySumNs: a.LatencySumNs,
			LatencyMaxNs: a.LatencyMaxNs,
			AllocBytes:   a.AllocBytes,
			FreeBytes:    a.FreeBytes,
			Errors:       a.Errors,
			UvmFaults:    a.UvmFaults,
			UvmEvicts:    a.UvmEvicts,
		}
	}
	return dst
}

// toWireEvent converts source Event to wire Event.
func toWireEvent(src source.Event) wire.Event {
	return wire.Event{
		TsNano:          src.TsNano,
		StartBoottimeNs: src.StartBoottimeNs,
		LatencyNs:       src.LatencyNs,
		Address:         src.Address,
		Bytes:           src.Bytes,
		Tgid:            src.Tgid,
		Tid:             src.Tid,
		DeviceOrdinal:   src.DeviceOrdinal,
		ApiID:           src.ApiID,
		Flags:           src.Flags,
		Status:          src.Status,
	}
}

// broadcastLoop selects over all five source channels, encodes each message
// once using the wire package, and offers it to every connected client's
// per-client channel. Slow clients lose their oldest messages; they never stall others.
func (s *SocketServer) broadcastLoop() {
	defer s.wg.Done()

	for {
		var msg []byte
		var msgType string
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
			msgType = wire.TypeGPU
			msg, err = wire.Encode(wire.TypeGPU, gpus[0].TsNano, toWireGPUSamples(gpus))

		case pl, ok := <-s.sock.Procs:
			if !ok {
				return
			}
			msgType = wire.TypeProcs
			msg, err = wire.Encode(wire.TypeProcs, pl.TsNano, toWireProcList(pl))

		case xid, ok := <-s.sock.Xid:
			if !ok {
				return
			}
			msgType = wire.TypeXid
			msg, err = wire.Encode(wire.TypeXid, xid.TsNano, toWireXid(xid))

		case agg, ok := <-s.sock.Agg:
			if !ok {
				return
			}
			if len(agg) == 0 {
				continue
			}
			msgType = wire.TypeAgg
			msg, err = wire.Encode(wire.TypeAgg, agg[0].TsNano, toWireAggRows(agg))

		case ev, ok := <-s.sock.Event:
			if !ok {
				return
			}
			msgType = wire.TypeEvent
			msg, err = wire.Encode(wire.TypeEvent, int64(ev.TsNano), toWireEvent(ev))
		}

		if err != nil {
			log.Printf("socket: encode message: %v", err)
			continue
		}

		// Append newline so readers can use bufio.Scanner.
		msg = append(msg, '\n')

		s.mu.Lock()
		for _, queues := range s.clients {
			queue := queues.other
			if msgType == "gpu" {
				queue = queues.gpu
			}
			if sendToClient(queue, msg) {
				if msgType == "gpu" {
					source.DroppedGPU.Add(1)
				} else {
					source.DroppedOther.Add(1)
				}
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
	for conn, queues := range s.clients {
		close(queues.gpu)
		close(queues.other)
		conn.Close()
		delete(s.clients, conn)
		// Account for the clients torn down here, which never reach
		// removeClient because the map entry is already gone.
		source.Clients.Add(-1)
	}
	s.mu.Unlock()

	s.wg.Wait()
	os.Remove(s.path)
	return nil
}
