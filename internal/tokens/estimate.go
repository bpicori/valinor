package tokens

import (
	"strings"

	agentcontext "valinor/internal/context"
)

const (
	baseMessageOverheadTokens = int64(4)
	conversationEndOverhead   = int64(2)
)

// EstimatePromptTokens estimates prompt/context token usage from messages.
// It is a lightweight, provider-agnostic approximation for preflight and resume UI.
func EstimatePromptTokens(messages []agentcontext.Message) int64 {
	if len(messages) == 0 {
		return 0
	}

	var total int64
	for _, msg := range messages {
		total += baseMessageOverheadTokens
		total += estimateTextTokens(msg.Content)
		total += estimateTextTokens(string(msg.Role))

		if msg.ToolCallID != "" {
			total += estimateTextTokens(msg.ToolCallID)
		}
		for _, call := range msg.ToolCalls {
			total += estimateTextTokens(call.ID)
			total += estimateTextTokens(call.Name)
			total += estimateTextTokens(call.Arguments)
		}
	}
	total += conversationEndOverhead
	return total
}

func estimateTextTokens(s string) int64 {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0
	}
	// Rough heuristic: ~4 characters per token for mixed English/code text.
	runes := int64(len([]rune(trimmed)))
	return (runes + 3) / 4
}
