// Package gateway provides the WebSocket gateway for client connections.
package gateway

import (
	"fmt"

	"valinor/internal/event"
)

// Protocol constants.
const (
	ProtocolVersion = 1

	// Client types (extensible for future clients).
	ClientTypeTUI      = "tui"
	ClientTypeWeb      = "web"
	ClientTypeTelegram = "telegram"
	ClientTypeWhatsApp = "whatsapp"
)

// ValidClientTypes contains all allowed client type values.
var ValidClientTypes = map[string]bool{
	ClientTypeTUI:      true,
	ClientTypeWeb:      true,
	ClientTypeTelegram: true,
	ClientTypeWhatsApp: true,
}

// ClientIdentity uniquely identifies a client connection.
type ClientIdentity struct {
	Type       string // ClientTypeTUI, ClientTypeWeb, etc.
	Identity   string // client-specific identity string
	Connection string // connection-specific identifier (for logging/debugging)
}

// Key returns the combined identity key used for session mapping.
func (ci ClientIdentity) Key() string {
	return ci.Type + ":" + ci.Identity
}

// SessionKey returns the public session label format.
func (ci ClientIdentity) SessionKey(sessionID string) string {
	// Use short session ID prefix for readability
	if len(sessionID) > 8 {
		return "session:" + ci.Type + ":" + sessionID[:8]
	}
	return "session:" + ci.Type + ":" + sessionID
}

// Command types (client -> gateway) - re-exported from event package.
const (
	CommandUserMessage           = string(event.UserMessage)
	CommandNewConversation       = string(event.NewConversation)
	CommandChangeProviderModel   = string(event.ChangeProviderModel)
	CommandCreateNewSession      = string(event.CreateNewSession)
	CommandRequestProviderModels = string(event.RequestProviderModels)
	CommandCancelAgentRun        = string(event.CancelAgentRun)
)

// Event types (gateway -> client) - re-exported from event package.
const (
	EventAgentContentDelta    = string(event.AgentContentDelta)
	EventAgentContentFull     = string(event.AgentContentFull)
	EventAgentImage           = string(event.AgentImage)
	EventAgentThinking        = string(event.AgentThinking)
	EventAgentStepTokens      = string(event.AgentStepTokens)
	EventContextTokens        = string(event.ContextTokens)
	EventAgentRefusal         = string(event.AgentRefusal)
	EventAgentToolCallStart   = string(event.AgentToolCallStart)
	EventAgentToolCallDone    = string(event.AgentToolCallDone)
	EventAgentDone            = string(event.AgentDone)
	EventAgentError           = string(event.AgentError)
	EventAgentCancelled       = string(event.AgentCancelled)
	EventProviderModelsResult = string(event.ProviderModelsResult)
	EventSessionAttached      = string(event.SessionAttached)
	EventNewConversation      = string(event.NewConversation)
	EventChangeProviderModel  = string(event.ChangeModelEvent)
	EventCronJobResult        = string(event.CronJobResult)
)

// Handshake types.
const (
	HandshakeClientHello = "client_hello"
	HandshakeHelloAck    = "hello_ack"
)

// ClientHelloPayload is the first message from a connecting client.
type ClientHelloPayload struct {
	ProtocolVersion int    `json:"protocol_version"`
	ClientType      string `json:"client_type"`
	ClientIdentity  string `json:"client_identity"`
	ResumeHint      string `json:"resume_hint,omitempty"` // optional, for future use
}

// HelloAckPayload is the gateway response to client hello.
type HelloAckPayload struct {
	Success         bool   `json:"success"`
	Error           string `json:"error,omitempty"`
	SessionKey      string `json:"session_key,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ContextTokens   int64  `json:"context_tokens,omitempty"`
	TokensEstimated bool   `json:"tokens_estimated,omitempty"`
}

// Command payloads - re-exported from event package.
type (
	UserMessagePayload           = event.UserMessagePayload
	ChangeProviderModelPayload   = event.ChangeProviderModelPayload
	CreateNewSessionPayload      = event.CreateNewSessionPayload
	RequestProviderModelsPayload = event.RequestProviderModelsPayload
)

// Event payloads - re-exported from event package.
type (
	ContentDeltaPayload         = event.ContentDeltaPayload
	ContentFullPayload          = event.ContentFullPayload
	ImagePayload                = event.ImagePayload
	ThinkingPayload             = event.ThinkingPayload
	StepTokenUsagePayload       = event.StepTokenUsagePayload
	ContextTokensPayload        = event.ContextTokensPayload
	RefusalPayload              = event.RefusalPayload
	ToolCallStartPayload        = event.ToolCallStartPayload
	ToolCallDonePayload         = event.ToolCallDonePayload
	AgentDonePayload            = event.AgentDonePayload
	AgentErrorPayload           = event.AgentErrorPayload
	AgentCancelledPayload       = event.AgentCancelledPayload
	ProviderModel               = event.ProviderModel
	ProviderModelsResultPayload = event.ProviderModelsResultPayload
)

// SessionAttachedPayload is re-exported from event package.
type SessionAttachedPayload = event.SessionAttachedPayload

// CronJobResultPayload is re-exported from event package.
type CronJobResultPayload = event.CronJobResultPayload

// Command represents a parsed client command with typed payload.
type Command struct {
	Type      string
	RequestID string
	Identity  ClientIdentity
	Payload   any // typed payload based on Type
}

// Event represents a gateway event to be sent to clients.
// Now a direct alias to event.Event, eliminating conversion boilerplate.
type Event = event.Event

// FromBusEvent converts a bus event to a gateway event.
// Now a no-op since both use the same event.Event type.
func FromBusEvent(be event.Event) Event {
	return be
}

// ValidateClientIdentity checks if a client identity is valid.
// Returns an error if the identity is invalid.
func ValidateClientIdentity(identity ClientIdentity) error {
	if identity.Type == "" {
		return fmt.Errorf("client_type is required")
	}

	if !ValidClientTypes[identity.Type] {
		return fmt.Errorf("invalid client_type: %s", identity.Type)
	}

	if identity.Identity == "" {
		return fmt.Errorf("client_identity is required")
	}

	return nil
}
