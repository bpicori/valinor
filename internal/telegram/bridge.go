// Package telegram provides the WebSocket bridge for gateway communication.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"valinor/internal/event"
	"valinor/internal/gateway"
	"valinor/internal/logger"
)

const (
	// dialTimeout is the timeout for initial WebSocket dial.
	dialTimeout = 10 * time.Second
	// handshakeTimeout is the timeout for handshake response.
	handshakeTimeout = 5 * time.Second
	// maxReconnectBackoff is the maximum exponential backoff for reconnection.
	maxReconnectBackoff = 30 * time.Second
	// initialReconnectBackoff is the starting backoff duration.
	initialReconnectBackoff = 1 * time.Second
)

// EventHandler is called when an event is received from the gateway.
type EventHandler func(ev gateway.Event)

// Bridge manages WebSocket communication with the gateway.
type Bridge struct {
	// Connection
	conn       *websocket.Conn
	gatewayURL string
	identity   string // Telegram client identity (chat-based)

	// Event handler
	eventHandler EventHandler

	// Outgoing channel for commands to gateway
	outgoing chan *gateway.WireEnvelope

	// State
	mu         sync.RWMutex
	connected  bool
	handshaked bool
	closed     bool
	closeOnce  sync.Once
	sessionKey string
	provider   string
	model      string

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewBridge creates a new WebSocket bridge for Telegram.
func NewBridge(gatewayURL, identity string, handler EventHandler) *Bridge {
	return &Bridge{
		gatewayURL:   gatewayURL,
		identity:     identity,
		eventHandler: handler,
		outgoing:     make(chan *gateway.WireEnvelope, gateway.OutgoingQueueSize),
	}
}

// Start connects to the gateway and starts the bridge.
func (b *Bridge) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	// Connect to WebSocket
	if err := b.connect(); err != nil {
		return fmt.Errorf("connect to gateway: %w", err)
	}

	// Start read and write loops
	b.wg.Add(2)
	go b.readLoop()
	go b.writeLoop()

	return nil
}

// Stop gracefully stops the bridge.
func (b *Bridge) Stop() {
	b.closeOnce.Do(func() {
		logger.Debug("telegram bridge: stopping", nil)

		// Mark as closed first so goroutines know to exit
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()

		b.cancel()

		if b.conn != nil {
			b.conn.Close()
		}

		b.wg.Wait()

		logger.Info("telegram bridge: stopped", nil)
	})
}

// IsConnected returns true if the bridge is connected and handshaked.
func (b *Bridge) IsConnected() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected && b.handshaked
}

// SessionInfo returns the handshake session metadata if available.
func (b *Bridge) SessionInfo() (sessionKey, provider, model string, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.connected || !b.handshaked || b.sessionKey == "" {
		return "", "", "", false
	}
	return b.sessionKey, b.provider, b.model, true
}

// IsClosed returns true if the bridge has been closed.
func (b *Bridge) IsClosed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.closed
}

// SendCommand sends a command to the gateway.
func (b *Bridge) SendCommand(cmdType string, payload interface{}, requestID string) bool {
	if b.IsClosed() {
		return false
	}

	payloadData, err := json.Marshal(payload)
	if err != nil {
		logger.Error("telegram bridge: failed to marshal command payload",
			logger.BuildMetadata("error", err, "command_type", cmdType))
		return false
	}

	env := &gateway.WireEnvelope{
		Kind:      gateway.KindCommand,
		Type:      cmdType,
		RequestID: requestID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payloadData,
	}

	select {
	case b.outgoing <- env:
		return true
	default:
		logger.Warning("telegram bridge: outgoing queue full, command dropped",
			logger.BuildMetadata("command_type", cmdType))
		return false
	}
}

// connect establishes the WebSocket connection and performs handshake.
func (b *Bridge) connect() error {
	// Parse and validate URL
	u, err := url.Parse(b.gatewayURL)
	if err != nil {
		return fmt.Errorf("invalid gateway URL: %w", err)
	}

	// Connect with timeout
	dialCtx, cancel := context.WithTimeout(b.ctx, dialTimeout)
	defer cancel()

	dialer := websocket.Dialer{
		HandshakeTimeout: dialTimeout,
	}

	conn, _, err := dialer.DialContext(dialCtx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial gateway: %w", err)
	}

	b.conn = conn

	// Send client_hello with telegram client type
	helloPayload := gateway.ClientHelloPayload{
		ProtocolVersion: gateway.ProtocolVersion,
		ClientType:      gateway.ClientTypeTelegram,
		ClientIdentity:  b.identity,
	}

	helloData, _ := json.Marshal(helloPayload)
	hello := &gateway.WireEnvelope{
		Kind:      gateway.KindHandshake,
		Type:      gateway.HandshakeClientHello,
		Timestamp: time.Now().UnixMilli(),
		Payload:   helloData,
	}

	if err := b.conn.WriteJSON(hello); err != nil {
		conn.Close()
		return fmt.Errorf("send client_hello: %w", err)
	}

	// Wait for hello_ack with timeout
	b.conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	var ack gateway.WireEnvelope
	for {
		var env gateway.WireEnvelope
		if err := b.conn.ReadJSON(&env); err != nil {
			conn.Close()
			return fmt.Errorf("read hello_ack: %w", err)
		}

		if env.Kind == gateway.KindHandshake && env.Type == gateway.HandshakeHelloAck {
			ack = env
			break
		}

		if env.Kind == gateway.KindError {
			conn.Close()
			return fmt.Errorf("gateway handshake error (%s): %s", env.Type, env.Error)
		}

		logger.Debug("telegram bridge: skipping pre-ack frame",
			logger.BuildMetadata("kind", env.Kind, "type", env.Type))
	}
	b.conn.SetReadDeadline(time.Time{}) // Clear deadline

	// Parse ack payload
	var ackPayload gateway.HelloAckPayload
	if err := json.Unmarshal(ack.Payload, &ackPayload); err != nil {
		conn.Close()
		return fmt.Errorf("parse hello_ack: %w", err)
	}

	if !ackPayload.Success {
		conn.Close()
		return fmt.Errorf("handshake rejected: %s", ackPayload.Error)
	}

	b.mu.Lock()
	b.connected = true
	b.handshaked = true
	b.sessionKey = ackPayload.SessionKey
	b.provider = ackPayload.Provider
	b.model = ackPayload.Model
	b.mu.Unlock()

	logger.Info("telegram bridge: connected",
		logger.BuildMetadata("session_key", ackPayload.SessionKey,
			"provider", ackPayload.Provider,
			"model", ackPayload.Model))

	return nil
}

// readLoop reads messages from the WebSocket and forwards to the event handler.
func (b *Bridge) readLoop() {
	defer b.wg.Done()
	defer logger.Debug("telegram bridge: read loop exited", nil)

	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		var env gateway.WireEnvelope
		if err := b.conn.ReadJSON(&env); err != nil {
			if b.IsClosed() {
				return
			}

			logger.Warning("telegram bridge: read error", logger.BuildMetadata("error", err))

			// Attempt reconnection
			if err := b.reconnect(); err != nil {
				logger.Error("telegram bridge: reconnection failed", logger.BuildMetadata("error", err))
				return
			}

			continue
		}

		// Handle different message kinds
		switch env.Kind {
		case gateway.KindEvent:
			// Convert to router event and forward
			ev, err := b.parseEvent(env)
			if err != nil {
				logger.Warning("telegram bridge: failed to parse event",
					logger.BuildMetadata("error", err, "type", env.Type))
				continue
			}
			if b.eventHandler != nil {
				b.eventHandler(ev)
			}

		case gateway.KindError:
			logger.Warning("telegram bridge: received error from gateway",
				logger.BuildMetadata("error_type", env.Type, "error", env.Error))

		default:
			logger.Debug("telegram bridge: unexpected message kind",
				logger.BuildMetadata("kind", env.Kind, "type", env.Type))
		}
	}
}

// writeLoop sends messages from the outgoing queue to the WebSocket.
func (b *Bridge) writeLoop() {
	defer b.wg.Done()
	defer logger.Debug("telegram bridge: write loop exited", nil)

	for {
		select {
		case <-b.ctx.Done():
			return

		case env, ok := <-b.outgoing:
			if !ok {
				return
			}

			if err := b.conn.WriteJSON(env); err != nil {
				logger.Warning("telegram bridge: write error", logger.BuildMetadata("error", err))

				// Try to reconnect
				if err := b.reconnect(); err != nil {
					logger.Error("telegram bridge: reconnection after write error failed",
						logger.BuildMetadata("error", err))
					return
				}

				// Retry sending the message
				if err := b.conn.WriteJSON(env); err != nil {
					logger.Warning("telegram bridge: retry write failed, dropping message",
						logger.BuildMetadata("error", err))
				}
			}
		}
	}
}

// reconnect attempts to reconnect to the gateway with backoff.
func (b *Bridge) reconnect() error {
	b.mu.Lock()
	b.connected = false
	b.handshaked = false
	b.mu.Unlock()

	// Exponential backoff: 1s, 2s, 4s, 8s, max 30s
	backoff := initialReconnectBackoff

	for attempts := 0; attempts < 10; attempts++ {
		logger.Info("telegram bridge: attempting reconnect",
			logger.BuildMetadata("attempt", attempts+1, "backoff", backoff))

		if err := b.connect(); err == nil {
			// Reconnection successful
			return nil
		}

		// Wait for backoff duration, but allow cancellation
		select {
		case <-b.ctx.Done():
			return fmt.Errorf("reconnect cancelled: %w", b.ctx.Err())
		case <-time.After(backoff):
			// Continue to next attempt
		}

		backoff *= 2
		if backoff > maxReconnectBackoff {
			backoff = maxReconnectBackoff
		}
	}

	return fmt.Errorf("max reconnection attempts exceeded")
}

// parseEvent converts a wire envelope to a gateway event.
// Uses the event package's ParseEvent function to avoid duplicating type mapping logic.
func (b *Bridge) parseEvent(env gateway.WireEnvelope) (gateway.Event, error) {
	ev, err := event.ParseEvent(env.Type, env.Payload)
	if err != nil {
		return gateway.Event{}, fmt.Errorf("parse event %s: %w", env.Type, err)
	}
	ev.RequestID = env.RequestID
	return ev, nil
}
