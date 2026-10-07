// Package httpd provides the HTTP + WebSocket server that serves the UI
// and bridges to the daemon socket.
package httpd

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/internal/wire"
	"github.com/Ebraam-Ashraf/wedjat/ui/server/client"
	"github.com/Ebraam-Ashraf/wedjat/ui/server/data"
	"github.com/gorilla/websocket"
)

//go:embed dist/*
var distFS embed.FS

// Server is the HTTP + WebSocket server.
type Server struct {
	addr       string
	dataDir    string
	socketPath string

	mu         sync.Mutex
	daemon     *client.Client
	data       *data.Data
	upgrader   websocket.Upgrader
	clients    map[*wsClient]bool
	shutdownCh chan struct{}
}

// wsClient represents a connected WebSocket client.
type wsClient struct {
	conn   *websocket.Conn
	send   chan []byte
	server *Server
}

// Config holds server configuration.
type Config struct {
	Addr       string // listen address (default: 127.0.0.1:3000)
	DataDir    string // daemon data directory
	SocketPath string // daemon socket path
}

// New creates a new HTTP server.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:3000"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/wedjat"
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = "/run/wedjatd/socket"
	}

	ctx := context.Background()
	d, err := data.New(ctx, cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open data: %w", err)
	}

	s := &Server{
		addr:       cfg.Addr,
		dataDir:    cfg.DataDir,
		socketPath: cfg.SocketPath,
		data:       d,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Only allow same-origin (127.0.0.1) for security
				origin := r.Header.Get("Origin")
				if origin == "" {
					return false
				}
				// Allow localhost origins
				return strings.HasPrefix(origin, "http://127.0.0.1:") ||
					strings.HasPrefix(origin, "http://localhost:")
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		clients:    make(map[*wsClient]bool),
		shutdownCh: make(chan struct{}),
	}

	return s, nil
}

// Start starts the HTTP server and daemon client.
func (s *Server) Start(ctx context.Context) error {
	// Start daemon client
	s.daemon = client.New(s.socketPath)
	s.daemon.OnGPU = s.broadcastGPU
	s.daemon.OnProcs = s.broadcastProcs
	s.daemon.OnXid = s.broadcastXid
	s.daemon.OnAgg = s.broadcastAgg
	s.daemon.OnEvent = s.broadcastEvent

	if err := s.daemon.Start(ctx); err != nil {
		return fmt.Errorf("start daemon client: %w", err)
	}

	// HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/assets/", s.handleAssets)
	mux.HandleFunc("/socket", s.handleWebSocket)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/gpus", s.handleGPUs)
	mux.HandleFunc("/api/gpus/", s.handleGPUSamples)
	mux.HandleFunc("/api/processes", s.handleProcesses)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/aggregates", s.handleAggregates)
	mux.HandleFunc("/api/incidents", s.handleIncidents)
	mux.HandleFunc("/api/incidents/", s.handleIncident)
	mux.HandleFunc("/api/incidents/stats", s.handleIncidentStats)
	mux.HandleFunc("/api/incidents/recent", s.handleRecentIncidents)

	// Debug endpoints (only in debug mode)
	if os.Getenv("WEDJAT_DEBUG") == "1" {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	server := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Start HTTP server
	go func() {
		log.Printf("HTTP server listening on %s", s.addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	// Wait for shutdown
	<-s.shutdownCh

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)
	s.daemon.Stop()
	s.data.Close()

	return nil
}

// Stop stops the server.
func (s *Server) Stop() {
	close(s.shutdownCh)
}

// handleIndex serves the embedded UI or 503 if not built.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Only serve index.html for root path
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Check if index.html exists in embedded FS
	file, err := distFS.Open("dist/index.html")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "UI not built. Run `make ui` to build the frontend.", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Get file info for content length
	fi, err := file.Stat()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Serve the file directly
	http.ServeContent(w, r, "index.html", fi.ModTime(), file.(io.ReadSeeker))
}

// handleAssets serves static assets from the embedded dist folder.
func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	// Remove /assets/ prefix
	path := strings.TrimPrefix(r.URL.Path, "/assets/")
	if path == "" || path == r.URL.Path {
		http.NotFound(w, r)
		return
	}

	file, err := distFS.Open("dist/assets/" + path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.ServeContent(w, r, path, fi.ModTime(), file.(io.ReadSeeker))
}

// handleWebSocket handles WebSocket connections for live data.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	c := &wsClient{
		conn:   conn,
		send:   make(chan []byte, 256),
		server: s,
	}

	s.mu.Lock()
	s.clients[c] = true
	clientCount := len(s.clients)
	s.mu.Unlock()

	log.Printf("WebSocket client connected (total: %d)", clientCount)

	// Start writer goroutine
	go c.writeLoop()

	// Read loop (handles ping/pong and close)
	go c.readLoop()

	// Notify daemon client about connection count change
	// The daemon could poll faster when clients are connected
}

// broadcastGPU broadcasts a GPU sample to all WebSocket clients.
func (s *Server) broadcastGPU(msg []wire.GPUSample) {
	env, err := wire.Encode(wire.TypeGPU, wire.NowUTC(), msg)
	if err != nil {
		return
	}
	s.broadcast(env)
}

// broadcastProcs broadcasts a process list to all WebSocket clients.
func (s *Server) broadcastProcs(msg *wire.ProcList) {
	env, err := wire.Encode(wire.TypeProcs, wire.NowUTC(), msg)
	if err != nil {
		return
	}
	s.broadcast(env)
}

// broadcastXid broadcasts an Xid event to all WebSocket clients.
func (s *Server) broadcastXid(msg *wire.Xid) {
	env, err := wire.Encode(wire.TypeXid, wire.NowUTC(), msg)
	if err != nil {
		return
	}
	s.broadcast(env)
}

// broadcastAgg broadcasts an aggregate row to all WebSocket clients.
func (s *Server) broadcastAgg(msg []wire.AggRow) {
	env, err := wire.Encode(wire.TypeAgg, wire.NowUTC(), msg)
	if err != nil {
		return
	}
	s.broadcast(env)
}

// broadcastEvent broadcasts a raw event to all WebSocket clients.
func (s *Server) broadcastEvent(msg *wire.Event) {
	env, err := wire.Encode(wire.TypeEvent, wire.NowUTC(), msg)
	if err != nil {
		return
	}
	s.broadcast(env)
}

// broadcast sends a message to all connected WebSocket clients.
func (s *Server) broadcast(msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
			// Client buffer full, drop message
		}
	}
}

// wsClient methods

func (c *wsClient) writeLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *wsClient) readLoop() {
	defer func() {
		c.server.removeClient(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
	}
}

func (s *Server) removeClient(c *wsClient) {
	s.mu.Lock()
	delete(s.clients, c)
	clientCount := len(s.clients)
	s.mu.Unlock()

	log.Printf("WebSocket client disconnected (total: %d)", clientCount)
}

// REST API handlers

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	status, err := s.data.GetStatus(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, status)
}

func (s *Server) handleGPUs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	gpus, err := s.data.ListGPUsResponse(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, gpus)
}

func (s *Server) handleGPUSamples(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse /api/gpus/{id}/samples
	path := strings.TrimPrefix(r.URL.Path, "/api/gpus/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "samples" {
		http.NotFound(w, r)
		return
	}

	// For now, just return 404 - will implement in A6
	http.NotFound(w, r)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	gpuID := parseInt64(r.URL.Query().Get("gpu_id"), 0)
	startTS := parseInt64(r.URL.Query().Get("start_ts"), 0)
	endTS := parseInt64(r.URL.Query().Get("end_ts"), 0)
	day := r.URL.Query().Get("day")

	if gpuID == 0 {
		http.Error(w, "gpu_id is required", http.StatusBadRequest)
		return
	}

	history, err := s.data.GetHistoryResponse(ctx, gpuID, startTS, endTS, day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, history)
}

func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	bootID := r.URL.Query().Get("boot_id")
	allUsers := r.URL.Query().Get("all_users") == "true"
	limit := parseInt(r.URL.Query().Get("limit"), 100)
	offset := parseInt(r.URL.Query().Get("offset"), 0)

	procs, err := s.data.ListProcessesResponse(ctx, bootID, allUsers, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, procs)
}

func (s *Server) handleAggregates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	procID := parseInt64(r.URL.Query().Get("proc_id"), 0)
	gpuID := parseInt64(r.URL.Query().Get("gpu_id"), 0)
	startTS := parseInt64(r.URL.Query().Get("start_ts"), 0)
	endTS := parseInt64(r.URL.Query().Get("end_ts"), 0)
	day := r.URL.Query().Get("day")

	aggs, err := s.data.GetAggregatesResponse(ctx, procID, gpuID, startTS, endTS, day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, aggs)
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	incidentType := r.URL.Query().Get("type")
	processID := parseInt64(r.URL.Query().Get("proc_id"), 0)
	gpuID := parseInt64(r.URL.Query().Get("gpu_id"), 0)
	startTS := parseInt64(r.URL.Query().Get("start_ts"), 0)
	endTS := parseInt64(r.URL.Query().Get("end_ts"), 0)
	limit := parseInt(r.URL.Query().Get("limit"), 10)
	offset := parseInt(r.URL.Query().Get("offset"), 0)

	incs, err := s.data.ListIncidentsResponse(ctx, incidentType, processID, gpuID, startTS, endTS, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, incs)
}

func (s *Server) handleIncident(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse /api/incidents/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/incidents/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	incidentID := parseInt64(path, 0)
	if incidentID == 0 {
		http.Error(w, "Invalid incident ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	inc, err := s.data.GetIncidentResponse(ctx, incidentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if inc == nil {
		http.NotFound(w, r)
		return
	}

	s.writeJSON(w, inc)
}

func (s *Server) handleIncidentStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	startTS := parseInt64(r.URL.Query().Get("start_ts"), 0)
	endTS := parseInt64(r.URL.Query().Get("end_ts"), 0)

	stats, err := s.data.IncidentsByType(ctx, startTS, endTS)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, stats)
}

func (s *Server) handleRecentIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	limit := parseInt(r.URL.Query().Get("limit"), 5)

	incs, err := s.data.RecentIncidentsResponse(ctx, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, incs)
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("JSON encode error: %v", err)
	}
}

// Helper functions

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func parseInt64(s string, def int64) int64 {
	if s == "" {
		return def
	}
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}

// Serve starts the server and blocks until shutdown.
func Serve(ctx context.Context, cfg Config) error {
	s, err := New(cfg)
	if err != nil {
		return err
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
			s.Stop()
		case <-ctx.Done():
			s.Stop()
		}
	}()

	return s.Start(ctx)
}