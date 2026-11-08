package llm

import (
	"context"

	"valinor/internal/tools"
)

// StreamDelta represents a chunk of streamed content from the provider.
type StreamDelta struct {
	Content string
	Refusal string
}

// StreamCallback is called by providers during streaming.
type StreamCallback func(delta StreamDelta)

// MessageRole represents the role of a message in a conversation.
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// Message represents a conversation message in a provider-agnostic format.
type Message struct {
	Role             MessageRole
	Content          string
	ReasoningContent string
	ToolCalls        []ToolCall
	ToolCallID       string
}

// ToolCall represents a request from the model to execute a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
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
	OnDelta   StreamCallback
}

// StreamResult contains the result of a streaming LLM request.
type StreamResult struct {
	Message Message
	Usage   Usage
}

// ModelInfo represents information about an available model.
type ModelInfo struct {
	ID          string
	DisplayName string
}

// AudioTranscribeParams holds parameters for a one-shot audio transcription request.
type AudioTranscribeParams struct {
	Model  string
	Audio  []byte
	Format string
}

// AudioTranscriber is implemented by providers that support speech-to-text.
type AudioTranscriber interface {
	TranscribeAudio(ctx context.Context, params AudioTranscribeParams) (string, error)
}

// Provider defines the interface that all LLM providers must implement.
type Provider interface {
	Stream(ctx context.Context, params StreamParams) (*StreamResult, error)
	Name() string
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
