// Package event provides the unified event type system and application-wide pub/sub bus
// used across router, gateway, and clients.
package event

import (
	"encoding/json"
	"fmt"
)

// EventType identifies the type of event.
// Using string constants for easier JSON serialization without conversion.
type EventType string

// Event types used across all layers.
const (
	// User intent events (published by UI).
	UserMessage           EventType = "user_message"            // User sent a chat message.
	NewConversation       EventType = "new_conversation"        // User requested /new or /clear.
	ChangeProviderModel   EventType = "change_provider_model"   // User changed model for the session.
	CreateNewSession      EventType = "create_new_session"      // User requested new session with different provider.
	RequestProviderModels EventType = "request_provider_models" // UI requests available models for a provider.
	CancelAgentRun        EventType = "cancel_agent_run"        // User requested cancellation of in-flight agent run.

	// Agent streaming events (published by Agent).
	ProviderModelsResult     EventType = "provider_models_result"     // Runtime returns available models.
	AgentContentDelta        EventType = "agent_content_delta"        // Streamed content chunk.
	AgentContentFull         EventType = "agent_content_full"         // Full assistant content for response.
	AgentImage               EventType = "agent_image"                // Generated image delivered to the client.
	AgentFile                EventType = "agent_file"                 // Workspace file delivered to the client.
	AgentQuestion            EventType = "agent_question"             // Structured question for the client.
	AgentThinking            EventType = "agent_thinking"             // Reasoning/thinking step.
	AgentStepTokens          EventType = "agent_step_tokens"          // Per-step token usage.
	ContextTokens            EventType = "context_tokens"             // Current context token estimate.
	ContextCompressed        EventType = "context_compressed"         // Middle context summarized.
	ContextCompressionFailed EventType = "context_compression_failed" // Context summarization failed.

	AgentRefusal       EventType = "agent_refusal"         // Model refusal.
	AgentToolCallStart EventType = "agent_tool_call_start" // Tool call initiated.
	AgentToolCallDone  EventType = "agent_tool_call_done"  // Tool call completed.
	AgentDone          EventType = "agent_done"            // Agent turn finished.
	AgentError         EventType = "agent_error"           // Agent error.
	AgentCancelled     EventType = "agent_cancelled"       // Agent run was cancelled.

	// Session lifecycle events.
	SessionAttached  EventType = "session_attached" // Session attached to identity.
	ChangeModelEvent EventType = "change_model"     // Provider/model changed notification.

	// Scheduled job events.
	CronJobResult EventType = "cron_job_result" // Scheduled cron job execution result.
)

// Event is a unified event with typed payload.
// Used by bus, router, and gateway without conversion.
type Event struct {
	Type      EventType
	Payload   any    // Typed payload based on Type.
	SessionID string // Optional: identifies the session that produced this event.
	RequestID string // Optional: correlation ID for request tracing.
}

// PayloadFactory creates a new payload instance for a given event type.
type PayloadFactory func() any

// Registry maps event types to their payload constructors.
var registry = map[EventType]PayloadFactory{
	UserMessage:           func() any { return &UserMessagePayload{} },
	NewConversation:       func() any { return nil },
	ChangeProviderModel:   func() any { return &ChangeProviderModelPayload{} },
	CreateNewSession:      func() any { return &CreateNewSessionPayload{} },
	RequestProviderModels: func() any { return &RequestProviderModelsPayload{} },
	CancelAgentRun:        func() any { return nil },

	ProviderModelsResult:     func() any { return &ProviderModelsResultPayload{} },
	AgentContentDelta:        func() any { return &ContentDeltaPayload{} },
	AgentContentFull:         func() any { return &ContentFullPayload{} },
	AgentImage:               func() any { return &ImagePayload{} },
	AgentFile:                func() any { return &FilePayload{} },
	AgentQuestion:            func() any { return &QuestionPayload{} },
	AgentThinking:            func() any { return &ThinkingPayload{} },
	AgentStepTokens:          func() any { return &StepTokenUsagePayload{} },
	ContextTokens:            func() any { return &ContextTokensPayload{} },
	ContextCompressed:        func() any { return &ContextCompressedPayload{} },
	ContextCompressionFailed: func() any { return &ContextCompressionFailedPayload{} },

	AgentRefusal:       func() any { return &RefusalPayload{} },
	AgentToolCallStart: func() any { return &ToolCallStartPayload{} },
	AgentToolCallDone:  func() any { return &ToolCallDonePayload{} },
	AgentDone:          func() any { return &AgentDonePayload{} },
	AgentError:         func() any { return &AgentErrorPayload{} },
	AgentCancelled:     func() any { return &AgentCancelledPayload{} },

	SessionAttached:  func() any { return &SessionAttachedPayload{} },
	ChangeModelEvent: func() any { return &ChangeProviderModelPayload{} },

	CronJobResult: func() any { return &CronJobResultPayload{} },
}

// ParseEvent parses an event from a type string and raw JSON payload.
// This eliminates the need for duplicate switch statements in ws_bridge.go.
func ParseEvent(eventType string, payloadData []byte) (Event, error) {
	event := Event{Type: EventType(eventType)}

	factory, ok := registry[EventType(eventType)]
	if !ok {
		return event, fmt.Errorf("unknown event type: %s", eventType)
	}

	payload := factory()
	if payload == nil {
		// No payload expected for this event type.
		return event, nil
	}

	if err := json.Unmarshal(payloadData, payload); err != nil {
		return event, fmt.Errorf("invalid payload for %s: %w", eventType, err)
	}

	// Dereference pointer to return value type for backward compatibility.
	event.Payload = deref(payload)
	return event, nil
}

// deref dereferences a pointer to get the value it points to.
// Returns the input unchanged if it's not a pointer.
func deref(v any) any {
	if v == nil {
		return nil
	}
	switch ptr := v.(type) {
	case *UserMessagePayload:
		return *ptr
	case *ChangeProviderModelPayload:
		return *ptr
	case *CreateNewSessionPayload:
		return *ptr
	case *RequestProviderModelsPayload:
		return *ptr
	case *ProviderModelsResultPayload:
		return *ptr
	case *ContentDeltaPayload:
		return *ptr
	case *ContentFullPayload:
		return *ptr
	case *ImagePayload:
		return *ptr
	case *FilePayload:
		return *ptr
	case *QuestionPayload:
		return *ptr
	case *ThinkingPayload:
		return *ptr
	case *RefusalPayload:
		return *ptr
	case *ToolCallStartPayload:
		return *ptr
	case *ToolCallDonePayload:
		return *ptr
	case *AgentDonePayload:
		return *ptr
	case *StepTokenUsagePayload:
		return *ptr
	case *ContextTokensPayload:
		return *ptr
	case *ContextCompressedPayload:
		return *ptr
	case *ContextCompressionFailedPayload:
		return *ptr
	case *AgentErrorPayload:
		return *ptr
	case *AgentCancelledPayload:
		return *ptr
	case *SessionAttachedPayload:
		return *ptr
	case *CronJobResultPayload:
		return *ptr
	default:
		return v
	}
}
