package llm

import (
	agentcontext "valinor/internal/context"
	"valinor/internal/tools"
)

// LLM runs model turns using the provided context runtime.
type LLM struct {
	provider        Provider
	model           string
	maxIterations   int
	maxOutputTokens int
}

// RunRequest configures a single run invocation.
type RunRequest struct {
	Messages   []agentcontext.Message
	Tools      []tools.Tool
	SessionID  string
	RequestID  string
	EmitEvents bool
	Compressor *Compressor
	// PriorPromptTokens is the provider-reported prompt size of the previous
	// turn, used as a floor for compression decisions.
	PriorPromptTokens int64
}

// RunResult returns the updated messages after the loop.
type RunResult struct {
	Messages []agentcontext.Message
	Usage    Usage
}