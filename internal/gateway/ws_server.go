// Package gateway provides the WebSocket gateway for client connections.
package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"valinor/internal/applog"
	"valinor/internal/router"
)

// Server is the WebSocket gateway server.
type Server struct {
	// Configuration
	Host string
	Port int
	Path string

	// Dependencies
	router router.Router

	// HTTP server
	server *http.Server
	mux    *http.ServeMux

	// Connection tracking
	mu          sync.RWMutex
	connections map[*ClientConnection]bool

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	started bool
}

// Config holds server configuration.
type Config struct {
	Host string
	Port int
	Path string
}

// DefaultConfig returns the default gateway configuration.
func DefaultConfig() Config {
	return Config{
		Host: "127.0.0.1",
		Port: 7778, // Different from web server port (7777)
		Path: "/ws",
	}
}

// NewServer creates a new WebSocket gateway server.
func NewServer(cfg Config, r router.Router) *Server {
	return &Server{
		Host:        cfg.Host,
		Port:        cfg.Port,
		Path:        cfg.Path,
		router:      r,
		connections: make(map[*ClientConnection]bool),
	}
}

// Start starts the WebSocket gateway server.
// This method blocks until the server is stopped or encounters an error.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("server already started")
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.mu.Unlock()

	// Create HTTP mux and register handlers
	s.mux = http.NewServeMux()
	s.mux.HandleFunc(s.Path, ServeWs(s.router, s))
	s.mux.HandleFunc("/health", s.healthHandler)

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start listener
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	applog.Info("gateway server starting",
		applog.M("address", addr, "path", s.Path))

	// Start server in a goroutine
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			applog.Error("gateway server error", applog.M("error", err))
		}
	}()

	// Wait for context cancellation or server error
	<-s.ctx.Done()

	return nil
}

// Stop gracefully stops the WebSocket gateway server.
func (s *Server) Stop() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}

	applog.Info("gateway server stopping", nil)

	// Cancel context to signal shutdown
	if s.cancel != nil {
		s.cancel()
	}

	// Close all connections
	for conn := range s.connections {
		conn.Close()
	}
	s.connections = make(map[*ClientConnection]bool)
	s.mu.Unlock()

	// Shutdown HTTP server with timeout
	if s.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			applog.Warning("gateway server shutdown error", applog.M("error", err))
		}
	}

	// Wait for goroutines to finish
	s.wg.Wait()

	applog.Info("gateway server stopped", nil)
	return nil
}

// addConnection registers a new client connection.
func (s *Server) addConnection(conn *ClientConnection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connections[conn] = true
}

// removeConnection unregisters a client connection.
func (s *Server) removeConnection(conn *ClientConnection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.connections, conn)
}

// ConnectionCount returns the number of active connections.
func (s *Server) ConnectionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connections)
}

// healthHandler handles health check requests.
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","connections":%d}`, s.ConnectionCount())
}

// URL returns the WebSocket URL for connecting clients.
func (s *Server) URL() string {
	return fmt.Sprintf("ws://%s:%d%s", s.Host, s.Port, s.Path)
}

// HTTPURL returns the HTTP URL for health checks.
func (s *Server) HTTPURL() string {
	return fmt.Sprintf("http://%s:%d/health", s.Host, s.Port)
}
