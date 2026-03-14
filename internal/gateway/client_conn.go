// Package gateway provides the WebSocket gateway for client connections.
package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"valinor/internal/logger"
)

// ClientConnection represents a single WebSocket client connection.
type ClientConnection struct {
	// Immutable fields (safe to read without lock)
	conn   *websocket.Conn
	router *Router
	server *Server

	// Mutable fields (require lock)
	mu        sync.RWMutex
	identity  ClientIdentity
	bound     bool               // true after successful hello
	outgoing  chan *WireEnvelope // bounded outgoing message queue
	closed    bool
	closeOnce sync.Once
	lastReqID string // last request ID for correlation

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
}

// NewClientConnection creates a new client connection wrapper.
func NewClientConnection(conn *websocket.Conn, r *Router, server *Server) *ClientConnection {
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
	defer logger.Debug("gateway: read loop exited", nil)

	// Set read limit
	c.conn.SetReadLimit(MaxMessageSize)

	// Set pong handler to detect dead connections
	c.conn.SetPongHandler(func(string) error {
		logger.Debug("gateway: pong received", nil)
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
				logger.Warning("gateway: unexpected close error",
					logger.BuildMetadata("error", err, "identity", c.getIdentityKey()))
			}
			return
		}

		// Parse envelope
		env, err := ParseEnvelope(data)
		if err != nil {
			logger.Warning("gateway: failed to parse envelope",
				logger.BuildMetadata("error", err, "identity", c.getIdentityKey()))
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
		logger.Warning("gateway: failed to read handshake", logger.BuildMetadata("error", err))
		return false
	}

	// Parse envelope
	env, err := ParseEnvelope(data)
	if err != nil {
		logger.Warning("gateway: invalid handshake envelope", logger.BuildMetadata("error", err))
		c.sendHandshakeResponse(false, fmt.Sprintf("Invalid message: %v", err), "", "", "", 0, false)
		return false
	}

	// Must be handshake kind
	if env.Kind != KindHandshake {
		logger.Warning("gateway: expected handshake, got", logger.BuildMetadata("kind", env.Kind))
		c.sendHandshakeResponse(false, "First message must be client_hello handshake", "", "", "", 0, false)
		return false
	}

	// Parse client hello
	hello, err := ParseClientHello(env)
	if err != nil {
		logger.Warning("gateway: invalid client_hello", logger.BuildMetadata("error", err))
		c.sendHandshakeResponse(false, fmt.Sprintf("Invalid client_hello: %v", err), "", "", "", 0, false)
		return false
	}

	// Create identity
	identity := ClientIdentity{
		Type:       hello.ClientType,
		Identity:   hello.ClientIdentity,
		Connection: c.conn.RemoteAddr().String(),
	}

	// Register with router (this creates/gets session)
	sess, err := c.router.SubscribeClient(c.ctx, identity, c)
	if err != nil {
		logger.Error("gateway: failed to subscribe client",
			logger.BuildMetadata("error", err, "identity", identity.Key()))
		c.sendHandshakeResponse(false, fmt.Sprintf("Failed to create session: %v", err), "", "", "", 0, false)
		return false
	}

	// Store identity and mark as bound
	c.mu.Lock()
	c.identity = identity
	c.bound = true
	c.mu.Unlock()

	// Send success response
	meta := sess.Meta()
	contextTokens, estimated := sess.ContextTokenCount()
	c.sendHandshakeResponse(true, "", identity.SessionKey(meta.ID), meta.Provider, meta.Model, contextTokens, estimated)

	logger.Info("gateway: client connected",
		logger.BuildMetadata("identity", identity.Key(),
			"session_id", meta.ID,
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

	// Store request ID for correlation with response events
	c.mu.Lock()
	c.lastReqID = env.RequestID
	c.mu.Unlock()

	// Parse command
	cmd, err := ParseCommand(env, identity)
	if err != nil {
		logger.Warning("gateway: invalid command",
			logger.BuildMetadata("error", err, "identity", identity.Key()))
		c.sendError("invalid_command", err.Error(), env.RequestID)
		return
	}

	// Handle command via router
	if err := c.router.HandleCommand(c.ctx, *cmd); err != nil {
		logger.Warning("gateway: command handling failed",
			logger.BuildMetadata("error", err, "command_type", cmd.Type, "identity", identity.Key()))
		c.sendError("command_failed", err.Error(), env.RequestID)
		return
	}

	logger.Debug("gateway: command handled",
		logger.BuildMetadata("command_type", cmd.Type, "identity", identity.Key(), "request_id", env.RequestID))
}

// writeLoop sends messages from the outgoing queue to the WebSocket.
func (c *ClientConnection) writeLoop() {
	defer logger.Debug("gateway: write loop exited", nil)

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
				logger.Warning("gateway: failed to write message",
					logger.BuildMetadata("error", err, "identity", c.getIdentityKey()))
				return
			}

		case <-ticker.C:
			// Send ping to keep connection alive
			c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				logger.Debug("gateway: ping failed", logger.BuildMetadata("error", err))
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

// SendEvent implements ClientEventConsumer.
// Returns true if event was queued, false if queue was full (event dropped).
// The event's RequestID is used for correlation if present; otherwise the last received request ID is used.
func (c *ClientConnection) SendEvent(event Event) bool {
	// Use event's request ID if present, otherwise fall back to last received request ID
	requestID := event.RequestID
	if requestID == "" {
		c.mu.RLock()
		requestID = c.lastReqID
		c.mu.RUnlock()
	}

	env := CreateEnvelope(event, requestID)

	select {
	case c.outgoing <- env:
		return true
	default:
		logger.Warning("gateway: outgoing queue full, event dropped",
			logger.BuildMetadata("event_type", event.Type, "identity", c.getIdentityKey()))
		return false
	}
}

// sendError sends an error response to the client.
func (c *ClientConnection) sendError(errorType, errorMsg, requestID string) {
	env := CreateErrorEnvelope(errorType, errorMsg, requestID)
	if !c.enqueueEnvelope(env, WriteTimeout) {
		logger.Warning("gateway: failed to send error (timeout)",
			logger.BuildMetadata("error_type", errorType, "identity", c.getIdentityKey()))
	}
}

// sendHandshakeResponse sends a hello_ack response.
func (c *ClientConnection) sendHandshakeResponse(success bool, errorMsg, sessionKey, provider, model string, contextTokens int64, estimated bool) {
	env := CreateHandshakeResponse(success, errorMsg, sessionKey, provider, model, contextTokens, estimated)
	if !c.enqueueEnvelope(env, WriteTimeout) {
		logger.Warning("gateway: failed to send handshake response",
			logger.BuildMetadata("identity", c.getIdentityKey()))
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

// Close implements ClientEventConsumer and closes the connection.
func (c *ClientConnection) Close() {
	c.closeOnce.Do(func() {
		logger.Debug("gateway: closing client connection", logger.BuildMetadata("identity", c.getIdentityKey()))

		// Cancel context to stop loops
		c.cancel()

		// Unsubscribe from router
		c.mu.RLock()
		if c.bound {
			c.router.UnsubscribeClient(c.identity, c)
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
func ServeWs(router *Router, server *Server) http.HandlerFunc {
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
			logger.Warning("gateway: websocket upgrade failed", logger.BuildMetadata("error", err))
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
