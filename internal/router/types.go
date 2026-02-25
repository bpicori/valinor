// Package router provides the internal API boundary for routing commands and events
// between clients (TUI, web, etc.) and the session core.
package router

import "valinor/internal/bus"

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

// Command types (client -> router).
const (
	CommandUserMessage           = "user_message"
	CommandNewConversation       = "new_conversation"
	CommandChangeProviderModel   = "change_provider_model"
	CommandCreateNewSession      = "create_new_session"
	CommandRequestProviderModels = "request_provider_models"
	CommandCancelAgentRun        = "cancel_agent_run"
)

// Event types (router -> client).
const (
	EventAgentContentDelta    = "agent_content_delta"
	EventAgentContentFull     = "agent_content_full"
	EventAgentThinking        = "agent_thinking"
	EventAgentStepTokens      = "agent_step_tokens"
	EventContextTokens        = "context_tokens"
	EventAgentRefusal         = "agent_refusal"
	EventAgentToolCallStart   = "agent_tool_call_start"
	EventAgentToolCallDone    = "agent_tool_call_done"
	EventAgentDone            = "agent_done"
	EventAgentError           = "agent_error"
	EventAgentCancelled       = "agent_cancelled"
	EventProviderModelsResult = "provider_models_result"
	EventSessionAttached      = "session_attached"
	EventNewConversation      = "new_conversation"
	EventChangeProviderModel  = "change_provider_model"
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

// Command payloads.

// UserMessagePayload carries a user chat message.
type UserMessagePayload struct {
	Content string `json:"content"`
}

// ChangeProviderModelPayload carries provider/model change request.
type ChangeProviderModelPayload struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// CreateNewSessionPayload carries provider and model for creating a new session.
type CreateNewSessionPayload struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// RequestProviderModelsPayload carries provider name to fetch available models for.
type RequestProviderModelsPayload struct {
	Provider string `json:"provider"`
}

// Event payloads.

// ContentDeltaPayload carries a streamed content chunk.
type ContentDeltaPayload struct {
	Content string `json:"content"`
}

// ContentFullPayload carries full assistant content for one response.
type ContentFullPayload struct {
	Content string `json:"content"`
}

// ThinkingPayload carries a reasoning/thinking update.
type ThinkingPayload struct {
	Step    int    `json:"step"`
	Content string `json:"content"`
}

// StepTokenUsagePayload carries per-step token usage.
type StepTokenUsagePayload struct {
	Step             int   `json:"step"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ContextTokensPayload carries current context token information.
type ContextTokensPayload struct {
	PromptTokens int64 `json:"prompt_tokens"`
	Estimated    bool  `json:"estimated"`
}

// RefusalPayload carries a model refusal message.
type RefusalPayload struct {
	Content string `json:"content"`
}

// ToolCallStartPayload carries metadata for an initiated tool call.
type ToolCallStartPayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	ToolArgs   string `json:"tool_args"`
}

// ToolCallDonePayload carries the result of a completed tool call.
type ToolCallDonePayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	ToolResult string `json:"tool_result"`
}

// AgentDonePayload carries token usage for a completed agent turn.
type AgentDonePayload struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// AgentErrorPayload carries an error from the agent.
type AgentErrorPayload struct {
	Error string `json:"error"`
}

// AgentCancelledPayload indicates the agent run was cancelled.
type AgentCancelledPayload struct{}

// ProviderModel describes a model option from a provider.
type ProviderModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ProviderModelsResultPayload carries available models fetched for a provider.
type ProviderModelsResultPayload struct {
	Provider string          `json:"provider"`
	Models   []ProviderModel `json:"models"`
	Error    string          `json:"error,omitempty"`
}

// SessionAttachedPayload indicates a session has been attached to this identity.
type SessionAttachedPayload struct {
	SessionKey string `json:"session_key"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
}

// Command represents a parsed client command with typed payload.
type Command struct {
	Type      string
	RequestID string
	Identity  ClientIdentity
	Payload   any // typed payload based on Type
}

// Event represents a router event to be sent to clients.
type Event struct {
	Type    string
	Payload any // typed payload based on Type
}

// FromBusEvent converts a legacy bus event to a router event.
func FromBusEvent(be bus.Event) Event {
	e := Event{}

	switch be.Type {
	case bus.EventAgentContentDelta:
		e.Type = EventAgentContentDelta
		if p, ok := be.Payload.(bus.ContentDeltaPayload); ok {
			e.Payload = ContentDeltaPayload{Content: p.Content}
		}
	case bus.EventAgentContentFull:
		e.Type = EventAgentContentFull
		if p, ok := be.Payload.(bus.ContentFullPayload); ok {
			e.Payload = ContentFullPayload{Content: p.Content}
		}
	case bus.EventAgentThinking:
		e.Type = EventAgentThinking
		if p, ok := be.Payload.(bus.ThinkingPayload); ok {
			e.Payload = ThinkingPayload{Step: p.Step, Content: p.Content}
		}
	case bus.EventAgentStepTokens:
		e.Type = EventAgentStepTokens
		if p, ok := be.Payload.(bus.StepTokenUsagePayload); ok {
			e.Payload = StepTokenUsagePayload{
				Step:             p.Step,
				PromptTokens:     p.PromptTokens,
				CompletionTokens: p.CompletionTokens,
				TotalTokens:      p.TotalTokens,
			}
		}
	case bus.EventContextTokens:
		e.Type = EventContextTokens
		if p, ok := be.Payload.(bus.ContextTokensPayload); ok {
			e.Payload = ContextTokensPayload{
				PromptTokens: p.PromptTokens,
				Estimated:    p.Estimated,
			}
		}
	case bus.EventAgentRefusal:
		e.Type = EventAgentRefusal
		if p, ok := be.Payload.(bus.RefusalPayload); ok {
			e.Payload = RefusalPayload{Content: p.Content}
		}
	case bus.EventAgentToolCallStart:
		e.Type = EventAgentToolCallStart
		if p, ok := be.Payload.(bus.ToolCallStartPayload); ok {
			e.Payload = ToolCallStartPayload{
				ToolCallID: p.ToolCallID,
				ToolName:   p.ToolName,
				ToolArgs:   p.ToolArgs,
			}
		}
	case bus.EventAgentToolCallDone:
		e.Type = EventAgentToolCallDone
		if p, ok := be.Payload.(bus.ToolCallDonePayload); ok {
			e.Payload = ToolCallDonePayload{
				ToolCallID: p.ToolCallID,
				ToolName:   p.ToolName,
				ToolResult: p.ToolResult,
			}
		}
	case bus.EventAgentDone:
		e.Type = EventAgentDone
		if p, ok := be.Payload.(bus.AgentDonePayload); ok {
			e.Payload = AgentDonePayload{
				PromptTokens:     p.PromptTokens,
				CompletionTokens: p.CompletionTokens,
				TotalTokens:      p.TotalTokens,
			}
		}
	case bus.EventAgentError:
		e.Type = EventAgentError
		if p, ok := be.Payload.(bus.AgentErrorPayload); ok {
			e.Payload = AgentErrorPayload{Error: p.Error.Error()}
		}
	case bus.EventAgentCancelled:
		e.Type = EventAgentCancelled
		e.Payload = AgentCancelledPayload{}
	case bus.EventProviderModelsResult:
		e.Type = EventProviderModelsResult
		if p, ok := be.Payload.(bus.ProviderModelsResultPayload); ok {
			models := make([]ProviderModel, 0, len(p.Models))
			for _, model := range p.Models {
				models = append(models, ProviderModel{
					ID:          model.ID,
					DisplayName: model.DisplayName,
				})
			}
			e.Payload = ProviderModelsResultPayload{
				Provider: p.Provider,
				Models:   models,
				Error:    p.Error,
			}
		}
	}

	return e
}
