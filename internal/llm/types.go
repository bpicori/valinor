package llm

import (
	agentcontext "valinor/internal/context"
	"valinor/internal/tools"
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
	Tools      []tools.Tool
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