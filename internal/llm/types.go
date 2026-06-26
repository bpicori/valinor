package llm

import (
	agentcontext "valinor/internal/context"
	"valinor/internal/llmapi"
	"valinor/internal/toolschema"
)

type (
	StreamDelta     = llmapi.StreamDelta
	StreamCallback  = llmapi.StreamCallback
	MessageRole     = llmapi.MessageRole
	Message         = llmapi.Message
	ToolCall        = llmapi.ToolCall
	Usage           = llmapi.Usage
	StreamParams    = llmapi.StreamParams
	StreamResult    = llmapi.StreamResult
	ModelInfo       = llmapi.ModelInfo
	Provider        = llmapi.Provider
)

const (
	RoleSystem    = llmapi.RoleSystem
	RoleUser      = llmapi.RoleUser
	RoleAssistant = llmapi.RoleAssistant
	RoleTool      = llmapi.RoleTool
)

// LLM runs model turns using the provided context runtime.
type LLM struct {
	provider      Provider
	model         string
	maxIterations int
}

// RunRequest configures a single run invocation.
type RunRequest struct {
	Messages   []agentcontext.Message
	Tools      []toolschema.Tool
	SessionID  string
	RequestID  string
	EmitEvents bool
	Compressor *Compressor
}

// RunResult returns the updated messages after the loop.
type RunResult struct {
	Messages []agentcontext.Message
	Usage    Usage
}