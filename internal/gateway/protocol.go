// Package gateway provides the WebSocket gateway for client connections.
package gateway

import (
	"encoding/json"
	"fmt"
	"time"

	"valinor/internal/router"
)

// WireEnvelope is the JSON structure sent over the WebSocket.
type WireEnvelope struct {
	Kind      string          `json:"kind"`                 // "command", "event", "handshake", "error"
	Type      string          `json:"type"`                 // specific message type
	RequestID string          `json:"request_id,omitempty"` // optional correlation ID
	Timestamp int64           `json:"timestamp"`            // Unix milliseconds
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     string          `json:"error,omitempty"` // for error responses
}

// Constants for envelope kinds.
const (
	KindCommand   = "command"
	KindEvent     = "event"
	KindHandshake = "handshake"
	KindError     = "error"
)

// MaxMessageSize is the maximum allowed WebSocket message size (1 MB).
const MaxMessageSize = 1024 * 1024

// WriteTimeout is the timeout for writing messages to the client.
const WriteTimeout = 10 * time.Second

// OutgoingQueueSize is the size of the per-client outgoing message buffer.
const OutgoingQueueSize = 64

// ParseEnvelope parses a raw JSON message into a WireEnvelope and validates it.
func ParseEnvelope(data []byte) (*WireEnvelope, error) {
	if len(data) > MaxMessageSize {
		return nil, fmt.Errorf("message too large: %d bytes (max %d)", len(data), MaxMessageSize)
	}

	var env WireEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Validate required fields
	if env.Kind == "" {
		return nil, fmt.Errorf("missing 'kind' field")
	}

	validKinds := map[string]bool{
		KindCommand:   true,
		KindEvent:     true,
		KindHandshake: true,
		KindError:     true,
	}
	if !validKinds[env.Kind] {
		return nil, fmt.Errorf("invalid 'kind': %s", env.Kind)
	}

	// Validate handshake has type
	if env.Kind == KindHandshake && env.Type == "" {
		return nil, fmt.Errorf("handshake requires 'type' field")
	}

	return &env, nil
}

// CreateEnvelope creates a wire envelope from a router event.
func CreateEnvelope(event router.Event, requestID string) *WireEnvelope {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		// Fall back to empty payload on marshal error
		payload = []byte("{}")
	}

	return &WireEnvelope{
		Kind:      KindEvent,
		Type:      string(event.Type),
		RequestID: requestID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

// CreateHandshakeResponse creates a hello_ack response envelope.
func CreateHandshakeResponse(success bool, errorMsg string, sessionKey, provider, model string, contextTokens int64, estimated bool) *WireEnvelope {
	payload := router.HelloAckPayload{
		Success:         success,
		Error:           errorMsg,
		SessionKey:      sessionKey,
		Provider:        provider,
		Model:           model,
		ContextTokens:   contextTokens,
		TokensEstimated: estimated,
	}

	data, _ := json.Marshal(payload)

	return &WireEnvelope{
		Kind:      KindHandshake,
		Type:      router.HandshakeHelloAck,
		Timestamp: time.Now().UnixMilli(),
		Payload:   data,
	}
}

// CreateErrorEnvelope creates an error response envelope.
func CreateErrorEnvelope(errorType, errorMsg, requestID string) *WireEnvelope {
	return &WireEnvelope{
		Kind:      KindError,
		Type:      errorType,
		RequestID: requestID,
		Timestamp: time.Now().UnixMilli(),
		Error:     errorMsg,
	}
}

// ParseClientHello parses the client_hello payload from an envelope.
func ParseClientHello(env *WireEnvelope) (*router.ClientHelloPayload, error) {
	if env.Type != router.HandshakeClientHello {
		return nil, fmt.Errorf("expected type '%s', got '%s'", router.HandshakeClientHello, env.Type)
	}

	var hello router.ClientHelloPayload
	if err := json.Unmarshal(env.Payload, &hello); err != nil {
		return nil, fmt.Errorf("invalid client_hello payload: %w", err)
	}

	// Validate protocol version
	if hello.ProtocolVersion != router.ProtocolVersion {
		return nil, fmt.Errorf("unsupported protocol version: %d (expected %d)",
			hello.ProtocolVersion, router.ProtocolVersion)
	}

	// Validate client type
	if hello.ClientType == "" {
		return nil, fmt.Errorf("client_type is required")
	}
	if !router.ValidClientTypes[hello.ClientType] {
		return nil, fmt.Errorf("invalid client_type: %s", hello.ClientType)
	}

	// Validate client identity
	if hello.ClientIdentity == "" {
		return nil, fmt.Errorf("client_identity is required")
	}

	return &hello, nil
}

// ParseCommand parses a command envelope into a router Command.
func ParseCommand(env *WireEnvelope, identity router.ClientIdentity) (*router.Command, error) {
	if env.Kind != KindCommand {
		return nil, fmt.Errorf("expected kind '%s', got '%s'", KindCommand, env.Kind)
	}

	cmd := &router.Command{
		Type:      env.Type,
		RequestID: env.RequestID,
		Identity:  identity,
	}

	// Parse payload based on command type
	switch env.Type {
	case router.CommandUserMessage:
		var p router.UserMessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("invalid user_message payload: %w", err)
		}
		if p.Content == "" {
			return nil, fmt.Errorf("user_message requires 'content' field")
		}
		cmd.Payload = p

	case router.CommandChangeProviderModel:
		var p router.ChangeProviderModelPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("invalid change_provider_model payload: %w", err)
		}
		if p.Provider == "" || p.Model == "" {
			return nil, fmt.Errorf("change_provider_model requires 'provider' and 'model' fields")
		}
		cmd.Payload = p

	case router.CommandCreateNewSession:
		var p router.CreateNewSessionPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("invalid create_new_session payload: %w", err)
		}
		if p.Provider == "" || p.Model == "" {
			return nil, fmt.Errorf("create_new_session requires 'provider' and 'model' fields")
		}
		cmd.Payload = p

	case router.CommandRequestProviderModels:
		var p router.RequestProviderModelsPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("invalid request_provider_models payload: %w", err)
		}
		if p.Provider == "" {
			return nil, fmt.Errorf("request_provider_models requires 'provider' field")
		}
		cmd.Payload = p

	case router.CommandNewConversation, router.CommandCancelAgentRun:
		// These commands have no payload
		cmd.Payload = nil

	default:
		return nil, fmt.Errorf("unknown command type: %s", env.Type)
	}

	return cmd, nil
}
