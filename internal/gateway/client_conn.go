// Package gateway provides the WebSocket gateway for client connections.
package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"valinor/internal/applog"
	"valinor/internal/router"
)

// ClientConnection represents a single WebSocket client connection.
type ClientConnection struct {
	// Immutable fields (safe to read without lock)
	conn     *websocket.Conn
	router   router.Router
	server   *Server

	// Mutable fields (require lock)
	mu           sync.RWMutex
	identity     router.ClientIdentity
	bound        bool           // true after successful hello
	outgoing     chan *WireEnvelope // bounded outgoing message queue
	closed       bool
	closeOnce    sync.Once

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
}

// NewClientConnection creates a new client connection wrapper.
func NewClientConnection(conn *websocket.Conn, r router.Router, server *Server) *ClientConnection {
	ctx, cancel := context.WithCancel(context.Background())

	return &ClientConnection{
		conn:     conn,
		router:   r,
		server:   server,
		outgoing: make(chan *WireEnvelope, OutgoingQueueSize),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Run starts the client connection handling.
// It blocks until the connection is closed.
func (c *ClientConnection) Run() {
	// Start read and write loops
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		c.readLoop()
	}()

	go func() {
		defer wg.Done()
		c.writeLoop()
	}()

	// Wait for both loops to finish
	wg.Wait()

	// Clean up
	c.Close()
}

// readLoop reads messages from the WebSocket and processes them.
func (c *ClientConnection) readLoop() {
	defer applog.Debug("gateway: read loop exited", nil)

	// Set read limit
	c.conn.SetReadLimit(MaxMessageSize)

	// Set pong handler to detect dead connections
	c.conn.SetPongHandler(func(string) error {
		applog.Debug("gateway: pong received", nil)
		return nil
	})

	// First message must be client_hello
	if !c.handleHandshake() {
		return
	}

	// Process subsequent messages
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		// Read message
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				applog.Warning("gateway: unexpected close error",
					applog.M("error", err, "identity", c.getIdentityKey()))
			}
			return
		}

		// Parse envelope
		env, err := ParseEnvelope(data)
		if err != nil {
			applog.Warning("gateway: failed to parse envelope",
				applog.M("error", err, "identity", c.getIdentityKey()))
			c.sendError("parse_error", fmt.Sprintf("Invalid message: %v", err), "")
			continue
		}

		// Handle based on kind
		switch env.Kind {
		case KindCommand:
			c.handleCommand(env)
		case KindHandshake:
			// Only allow hello during handshake, reject subsequent handshakes
			c.sendError("protocol_error", "Handshake already completed", env.RequestID)
		default:
			c.sendError("protocol_error", fmt.Sprintf("Unexpected kind: %s", env.Kind), env.RequestID)
		}
	}
}

// handleHandshake processes the initial client_hello message.
// Returns true if handshake succeeded, false otherwise.
func (c *ClientConnection) handleHandshake() bool {
	// Read first message
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		applog.Warning("gateway: failed to read handshake", applog.M("error", err))
		return false
	}

	// Parse envelope
	env, err := ParseEnvelope(data)
	if err != nil {
		applog.Warning("gateway: invalid handshake envelope", applog.M("error", err))
		c.sendHandshakeResponse(false, fmt.Sprintf("Invalid message: %v", err), "", "", "", 0, false)
		return false
	}

	// Must be handshake kind
	if env.Kind != KindHandshake {
		applog.Warning("gateway: expected handshake, got", applog.M("kind", env.Kind))
		c.sendHandshakeResponse(false, "First message must be client_hello handshake", "", "", "", 0, false)
		return false
	}

	// Parse client hello
	hello, err := ParseClientHello(env)
	if err != nil {
		applog.Warning("gateway: invalid client_hello", applog.M("error", err))
		c.sendHandshakeResponse(false, fmt.Sprintf("Invalid client_hello: %v", err), "", "", "", 0, false)
		return false
	}

	// Create identity
	identity := router.ClientIdentity{
		Type:       hello.ClientType,
		Identity:   hello.ClientIdentity,
		Connection: c.conn.RemoteAddr().String(),
	}

	// Register with router (this creates/gets session)
	binding, err := c.router.SubscribeClient(c.ctx, identity, c)
	if err != nil {
		applog.Error("gateway: failed to subscribe client",
			applog.M("error", err, "identity", identity.Key()))
		c.sendHandshakeResponse(false, fmt.Sprintf("Failed to create session: %v", err), "", "", "", 0, false)
		return false
	}

	// Store identity and mark as bound
	c.mu.Lock()
	c.identity = identity
	c.bound = true
	c.mu.Unlock()

	// Send success response
	meta := binding.Session.Meta()
	c.sendHandshakeResponse(true, "", binding.PublicKey, meta.Provider, meta.Model, 0, true)

	applog.Info("gateway: client connected",
		applog.M("identity", identity.Key(),
			"session_id", binding.SessionID,
			"remote_addr", c.conn.RemoteAddr()))

	return true
}

// handleCommand processes a command message.
func (c *ClientConnection) handleCommand(env *WireEnvelope) {
	c.mu.RLock()
	identity := c.identity
	bound := c.bound
	c.mu.RUnlock()

	if !bound {
		c.sendError("protocol_error", "Must complete handshake before sending commands", env.RequestID)
		return
	}

	// Parse command
	cmd, err := ParseCommand(env, identity)
	if err != nil {
		applog.Warning("gateway: invalid command",
			applog.M("error", err, "identity", identity.Key()))
		c.sendError("invalid_command", err.Error(), env.RequestID)
		return
	}

	// Handle command via router
	if err := c.router.HandleCommand(c.ctx, *cmd); err != nil {
		applog.Warning("gateway: command handling failed",
			applog.M("error", err, "command_type", cmd.Type, "identity", identity.Key()))
		c.sendError("command_failed", err.Error(), env.RequestID)
		return
	}

	applog.Debug("gateway: command handled",
		applog.M("command_type", cmd.Type, "identity", identity.Key()))
}

// writeLoop sends messages from the outgoing queue to the WebSocket.
func (c *ClientConnection) writeLoop() {
	defer applog.Debug("gateway: write loop exited", nil)

	// Start ping ticker
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return

		case env, ok := <-c.outgoing:
			if !ok {
				return
			}
			if err := c.writeEnvelope(env); err != nil {
				applog.Warning("gateway: failed to write message",
					applog.M("error", err, "identity", c.getIdentityKey()))
				return
			}

		case <-ticker.C:
			// Send ping to keep connection alive
			c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				applog.Debug("gateway: ping failed", applog.M("error", err))
				return
			}
		}
	}
}

// writeEnvelope writes a single envelope to the WebSocket.
func (c *ClientConnection) writeEnvelope(env *WireEnvelope) error {
	c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
	return c.conn.WriteJSON(env)
}

// SendEvent implements router.ClientEventSink.
// Returns true if event was queued, false if queue was full (event dropped).
func (c *ClientConnection) SendEvent(event router.Event) bool {
	env := CreateEnvelope(event, "")

	select {
	case c.outgoing <- env:
		return true
	default:
		applog.Warning("gateway: outgoing queue full, event dropped",
			applog.M("event_type", event.Type, "identity", c.getIdentityKey()))
		return false
	}
}

// sendError sends an error response to the client.
func (c *ClientConnection) sendError(errorType, errorMsg, requestID string) {
	env := CreateErrorEnvelope(errorType, errorMsg, requestID)
	if !c.enqueueEnvelope(env, WriteTimeout) {
		applog.Warning("gateway: failed to send error (timeout)",
			applog.M("error_type", errorType, "identity", c.getIdentityKey()))
	}
}

// sendHandshakeResponse sends a hello_ack response.
func (c *ClientConnection) sendHandshakeResponse(success bool, errorMsg, sessionKey, provider, model string, contextTokens int64, estimated bool) {
	env := CreateHandshakeResponse(success, errorMsg, sessionKey, provider, model, contextTokens, estimated)
	if !c.enqueueEnvelope(env, WriteTimeout) {
		applog.Warning("gateway: failed to send handshake response",
			applog.M("identity", c.getIdentityKey()))
	}
}

// enqueueEnvelope queues an outgoing envelope with timeout protection.
func (c *ClientConnection) enqueueEnvelope(env *WireEnvelope, timeout time.Duration) bool {
	select {
	case <-c.ctx.Done():
		return false
	case c.outgoing <- env:
		return true
	case <-time.After(timeout):
		return false
	}
}

// getIdentityKey returns the identity key for logging (safe even if not bound).
func (c *ClientConnection) getIdentityKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.bound {
		return "(unbound)"
	}
	return c.identity.Key()
}

// Close implements router.ClientEventSink and closes the connection.
func (c *ClientConnection) Close() {
	c.closeOnce.Do(func() {
		applog.Debug("gateway: closing client connection", applog.M("identity", c.getIdentityKey()))

		// Cancel context to stop loops
		c.cancel()

		// Unsubscribe from router
		c.mu.RLock()
		if c.bound {
			c.router.UnsubscribeClient(c.identity)
		}
		c.mu.RUnlock()

		// Close WebSocket
		c.conn.Close()

		// Close outgoing channel
		close(c.outgoing)

		// Notify server
		if c.server != nil {
			c.server.removeConnection(c)
		}
	})
}

// ServeWs handles WebSocket requests from clients.
func ServeWs(router router.Router, server *Server) http.HandlerFunc {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			// Allow all origins for local development
			// TODO: restrict origins in production
			return true
		},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			applog.Warning("gateway: websocket upgrade failed", applog.M("error", err))
			return
		}

		client := NewClientConnection(conn, router, server)
		if server != nil {
			server.addConnection(client)
		}

		// Start handling in a goroutine
		go client.Run()
	}
}
