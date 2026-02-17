package bus

// EventType identifies a bus event.
type EventType int

const (
	// User intent events (published by UI).
	EventUserMessage     EventType = iota // User sent a chat message.
	EventNewConversation                  // User requested /new or /clear.

	// Agent streaming events (published by Agent).
	EventAgentContentDelta // Streamed content chunk.
	EventAgentRefusal      // Model refusal.
	EventAgentToolCallStart // Tool call initiated.
	EventAgentToolCallDone  // Tool call completed.
	EventAgentDone          // Agent turn finished.
	EventAgentError         // Agent error.
)

// Event is a bus message with a typed payload.
type Event struct {
	Type    EventType
	Payload any
}

// --- Payload types ---

// UserMessagePayload carries the content of a user chat message.
type UserMessagePayload struct {
	Content string
}

// ContentDeltaPayload carries a streamed content chunk from the agent.
type ContentDeltaPayload struct {
	Content string
}

// RefusalPayload carries a model refusal message.
type RefusalPayload struct {
	Content string
}

// ToolCallStartPayload carries metadata for an initiated tool call.
type ToolCallStartPayload struct {
	ToolCallID string
	ToolName   string
	ToolArgs   string
}

// ToolCallDonePayload carries the result of a completed tool call.
type ToolCallDonePayload struct {
	ToolCallID string
	ToolName   string
	ToolResult string
}

// AgentDonePayload carries token usage for a completed agent turn.
type AgentDonePayload struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// AgentErrorPayload carries an error from the agent.
type AgentErrorPayload struct {
	Error error
}
