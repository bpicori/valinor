package llm

import (
	"context"

	agentcontext "valinor/internal/context"
	"valinor/internal/tools"
)

// StreamDelta represents a chunk of streamed content from the provider.
type StreamDelta struct {
	Content string // Text content delta
	Refusal string // Refusal/rejection content
}

// StreamCallback is called by providers during streaming to deliver content chunks.
// The LLM layer provides this callback to bridge to the event bus.
type StreamCallback func(delta StreamDelta)

// LLM runs model turns using the provided context runtime.
type LLM struct {
	provider      Provider
	model         string
	maxIterations int
}

// MessageRole represents the role of a message in a conversation.
type MessageRole string

// Message represents a conversation message in a provider-agnostic format.
type Message struct {
	Role       MessageRole
	Content    string
	ToolCalls  []ToolCall // For assistant messages with tool calls
	ToolCallID string     // For tool result messages
}

// ToolCall represents a request from the model to execute a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON string
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// StreamParams contains parameters for a streaming LLM request.
type StreamParams struct {
	Messages  []Message
	Tools     []tools.Tool
	Model     string
	MaxTokens int
	OnDelta   StreamCallback // Called during streaming for each content chunk
}

// StreamResult contains the result of a streaming LLM request.
type StreamResult struct {
	Message Message
	Usage   Usage
}

// ModelInfo represents information about an available model.
type ModelInfo struct {
	ID          string // Model identifier for API calls
	DisplayName string // Optional human-readable name
}

// Provider defines the interface that all LLM providers must implement.
type Provider interface {
	Stream(ctx context.Context, params StreamParams) (*StreamResult, error)

	// Name returns a human-readable identifier for this provider.
	Name() string

	// ListModels returns models available from this provider.
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// RunRequest configures a single run invocation.
type RunRequest struct {
	Messages  []agentcontext.Message
	Tools     []tools.Tool // Tools available for this run
	SessionID string       // Optional: identifies the session for event routing.
	RequestID string       // Optional: correlation ID for request tracing.
}

// RunResult returns the updated messages after the loop.
type RunResult struct {
	Messages []agentcontext.Message
}
