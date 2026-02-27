// Package tui provides the WebSocket transport bridge for the TUI client.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"

	"valinor/internal/event"
	"valinor/internal/gateway"
	"valinor/internal/logger"
	"valinor/internal/router"
)

// WebSocket timeout and backoff constants.
const (
	dialTimeout         = 10 * time.Second // timeout for initial WebSocket dial
	handshakeTimeout    = 5 * time.Second  // timeout for handshake response
	maxReconnectBackoff = 30 * time.Second // maximum exponential backoff for reconnection
)

// WSBridge is a WebSocket-based transport bridge for the TUI.
// It replaces the direct bus dependency with WebSocket communication to the gateway.
type WSBridge struct {
	// Connection
	conn       *websocket.Conn
	gatewayURL string
	identity   router.ClientIdentity

	// Bubble Tea program for sending messages
	program *tea.Program

	// Outgoing channel for commands to gateway
	outgoing chan *gateway.WireEnvelope

	// State
	mu         sync.RWMutex
	connected  bool
	handshaked bool
	closed     bool
	closeOnce  sync.Once

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// wsEventMsg wraps a gateway envelope for the Bubble Tea message loop.
type wsEventMsg struct {
	Type    string
	Payload json.RawMessage
}

// connectionStatusMsg signals connection state changes to the UI.
type connectionStatusMsg struct {
	Connected  bool
	Error      error
	SessionKey string
	Provider   string
	Model      string
}

// NewWSBridge creates a new WebSocket bridge for TUI.
// If identity is empty, "default" will be used.
func NewWSBridge(gatewayURL, identity string) *WSBridge {
	if identity == "" {
		identity = "default"
	}
	return &WSBridge{
		gatewayURL: gatewayURL,
		identity: router.ClientIdentity{
			Type:     router.ClientTypeTUI,
			Identity: identity,
		},
		outgoing: make(chan *gateway.WireEnvelope, gateway.OutgoingQueueSize),
	}
}

// Start connects to the gateway and starts the bridge.
// Must be called before using the bridge.
func (b *WSBridge) Start(ctx context.Context, p *tea.Program) error {
	b.program = p
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
func (b *WSBridge) Stop() {
	b.closeOnce.Do(func() {
		logger.Debug("tui ws bridge: stopping", nil)

		// Mark as closed first so goroutines know to exit
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()

		b.cancel()

		if b.conn != nil {
			b.conn.Close()
		}

		b.wg.Wait()

		logger.Info("tui ws bridge: stopped", nil)
	})
}

// connect establishes the WebSocket connection and performs handshake.
func (b *WSBridge) connect() error {
	// Parse and validate URL
	u, err := url.Parse(b.gatewayURL)
	if err != nil {
		return fmt.Errorf("invalid gateway URL: %w", err)
	}

	// Connect with timeout
	dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	dialer := websocket.Dialer{
		HandshakeTimeout: dialTimeout,
	}

	conn, _, err := dialer.DialContext(dialCtx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial gateway: %w", err)
	}

	b.conn = conn

	// Send client_hello
	helloPayload := router.ClientHelloPayload{
		ProtocolVersion: router.ProtocolVersion,
		ClientType:      b.identity.Type,
		ClientIdentity:  b.identity.Identity,
	}

	helloData, _ := json.Marshal(helloPayload)
	hello := &gateway.WireEnvelope{
		Kind:      gateway.KindHandshake,
		Type:      router.HandshakeClientHello,
		Timestamp: time.Now().UnixMilli(),
		Payload:   helloData,
	}

	if err := b.conn.WriteJSON(hello); err != nil {
		conn.Close()
		return fmt.Errorf("send client_hello: %w", err)
	}

	// Wait for hello_ack with timeout.
	// The gateway can emit early events during subscribe, so we keep reading
	// until hello_ack arrives (or the read deadline expires).
	b.conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	var ack gateway.WireEnvelope
	for {
		var env gateway.WireEnvelope
		if err := b.conn.ReadJSON(&env); err != nil {
			conn.Close()
			return fmt.Errorf("read hello_ack: %w", err)
		}

		if env.Kind == gateway.KindHandshake && env.Type == router.HandshakeHelloAck {
			ack = env
			break
		}

		if env.Kind == gateway.KindError {
			conn.Close()
			return fmt.Errorf("gateway handshake error (%s): %s", env.Type, env.Error)
		}

		logger.Debug("tui ws bridge: skipping pre-ack frame",
			logger.BuildMetadata("kind", env.Kind, "type", env.Type))
	}
	b.conn.SetReadDeadline(time.Time{}) // Clear deadline

	// Parse ack payload
	var ackPayload router.HelloAckPayload
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
	b.mu.Unlock()

	logger.Info("tui ws bridge: connected",
		logger.BuildMetadata("session_key", ackPayload.SessionKey,
			"provider", ackPayload.Provider,
			"model", ackPayload.Model))

	if b.program != nil {
		go b.program.Send(connectionStatusMsg{
			Connected:  true,
			SessionKey: ackPayload.SessionKey,
			Provider:   ackPayload.Provider,
			Model:      ackPayload.Model,
		})
	}

	return nil
}

// readLoop reads messages from the WebSocket and forwards to Bubble Tea.
func (b *WSBridge) readLoop() {
	defer b.wg.Done()
	defer logger.Debug("tui ws bridge: read loop exited", nil)

	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		var env gateway.WireEnvelope
		if err := b.conn.ReadJSON(&env); err != nil {
			if b.isClosed() {
				return
			}

			logger.Warning("tui ws bridge: read error", logger.BuildMetadata("error", err))

			// Signal connection lost to UI
			b.program.Send(connectionStatusMsg{Connected: false, Error: err})

			// Attempt reconnection
			if err := b.reconnect(); err != nil {
				logger.Error("tui ws bridge: reconnection failed", logger.BuildMetadata("error", err))
				return
			}

			continue
		}

		// Handle different message kinds
		switch env.Kind {
		case gateway.KindEvent:
			// Forward to Bubble Tea
			b.program.Send(wsEventMsg{
				Type:    env.Type,
				Payload: env.Payload,
			})

		case gateway.KindError:
			logger.Warning("tui ws bridge: received error from gateway",
				logger.BuildMetadata("error_type", env.Type, "error", env.Error))

		default:
			logger.Debug("tui ws bridge: unexpected message kind",
				logger.BuildMetadata("kind", env.Kind, "type", env.Type))
		}
	}
}

// writeLoop sends messages from the outgoing queue to the WebSocket.
func (b *WSBridge) writeLoop() {
	defer b.wg.Done()
	defer logger.Debug("tui ws bridge: write loop exited", nil)

	for {
		select {
		case <-b.ctx.Done():
			return

		case env, ok := <-b.outgoing:
			if !ok {
				return
			}

			if err := b.conn.WriteJSON(env); err != nil {
				logger.Warning("tui ws bridge: write error", logger.BuildMetadata("error", err))

				// Try to reconnect
				if err := b.reconnect(); err != nil {
					logger.Error("tui ws bridge: reconnection after write error failed",
						logger.BuildMetadata("error", err))
					return
				}

				// Retry sending the message
				if err := b.conn.WriteJSON(env); err != nil {
					logger.Warning("tui ws bridge: retry write failed, dropping message",
						logger.BuildMetadata("error", err))
				}
			}
		}
	}
}

// reconnect attempts to reconnect to the gateway with backoff.
func (b *WSBridge) reconnect() error {
	b.mu.Lock()
	b.connected = false
	b.handshaked = false
	b.mu.Unlock()

	// Exponential backoff: 1s, 2s, 4s, 8s, max 30s
	backoff := time.Second
	maxBackoff := maxReconnectBackoff

	for attempts := 0; attempts < 10; attempts++ {
		logger.Info("tui ws bridge: attempting reconnect",
			logger.BuildMetadata("attempt", attempts+1, "backoff", backoff))

		if err := b.connect(); err == nil {
			// Reconnection successful (connect() sends updated session/model state).
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
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	return fmt.Errorf("max reconnection attempts exceeded")
}

// SendCommand sends a command to the gateway.
func (b *WSBridge) SendCommand(cmdType string, payload any) bool {
	if b.isClosed() {
		return false
	}

	payloadData, err := json.Marshal(payload)
	if err != nil {
		logger.Error("tui ws bridge: failed to marshal command payload",
			logger.BuildMetadata("error", err, "command_type", cmdType))
		return false
	}

	env := &gateway.WireEnvelope{
		Kind:      gateway.KindCommand,
		Type:      cmdType,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payloadData,
	}

	select {
	case b.outgoing <- env:
		return true
	default:
		logger.Warning("tui ws bridge: outgoing queue full, command dropped",
			logger.BuildMetadata("command_type", cmdType))
		return false
	}
}

// SendUserMessage sends a user message command.
func (b *WSBridge) SendUserMessage(content string) bool {
	return b.SendCommand(router.CommandUserMessage, router.UserMessagePayload{Content: content})
}

// SendNewConversation sends a new conversation command.
func (b *WSBridge) SendNewConversation() bool {
	return b.SendCommand(router.CommandNewConversation, nil)
}

// SendChangeProviderModel sends a change provider/model command.
func (b *WSBridge) SendChangeProviderModel(provider, model string) bool {
	return b.SendCommand(router.CommandChangeProviderModel, router.ChangeProviderModelPayload{
		Provider: provider,
		Model:    model,
	})
}

// SendCreateNewSession sends a create new session command.
func (b *WSBridge) SendCreateNewSession(provider, model string, force bool) bool {
	return b.SendCommand(router.CommandCreateNewSession, router.CreateNewSessionPayload{
		Provider: provider,
		Model:    model,
		Force:    force,
	})
}

// SendRequestProviderModels sends a request for available models for a provider.
func (b *WSBridge) SendRequestProviderModels(provider string) bool {
	return b.SendCommand(router.CommandRequestProviderModels, router.RequestProviderModelsPayload{
		Provider: provider,
	})
}

// SendCancelAgentRun sends a cancel agent run command.
func (b *WSBridge) SendCancelAgentRun() bool {
	return b.SendCommand(router.CommandCancelAgentRun, nil)
}

// IsConnected returns true if the bridge is connected and handshaked.
func (b *WSBridge) IsConnected() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected && b.handshaked
}

// isClosed returns true if the bridge has been closed.
func (b *WSBridge) isClosed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.closed
}

// ParseWSEvent parses a wsEventMsg into a router Event.
// Delegates to the shared event package to eliminate duplication.
func ParseWSEvent(msg wsEventMsg) (router.Event, error) {
	ev, err := event.ParseEvent(msg.Type, msg.Payload)
	if err != nil {
		return router.Event{}, err
	}
	return ev, nil
}
