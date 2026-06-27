package llm

import (
	"context"
	"encoding/json"
	"errors"

	agentcontext "valinor/internal/context"
	"valinor/internal/logger"
	"valinor/internal/tools"
)

const memoryPassPrompt = "[System] Review this turn. If anything is worth remembering for future sessions, call memory_log or memory_update now. If nothing warrants saving, respond with exactly: NO_REPLY"

var readOnlyTools = map[string]bool{
	"read_file":      true,
	"list_directory": true,
	"grep":           true,
	"web_search":     true,
	"todo_read":      true,
	"google_search":  true,
}

// shouldRunMemoryPass reports whether a silent memory pass should run after the user-facing turn.
func shouldRunMemoryPass(toolsUsed map[string]struct{}) bool {
	if len(toolsUsed) == 0 {
		return false
	}
	for name := range toolsUsed {
		if name == "memory_log" || name == "memory_update" {
			return false
		}
		if !readOnlyTools[name] {
			return true
		}
	}
	return false
}

func filterMemoryTools(toolset []tools.Tool) []tools.Tool {
	out := make([]tools.Tool, 0, 2)
	for _, t := range toolset {
		if t.Name == "memory_log" || t.Name == "memory_update" {
			out = append(out, t)
		}
	}
	return out
}

// runSilentMemoryPass optionally persists memories without emitting client events
// or mutating the conversation transcript returned to the caller.
func (l *LLM) runSilentMemoryPass(ctx context.Context, req RunRequest, messages []agentcontext.Message) Usage {
	memoryTools := filterMemoryTools(req.Tools)
	if len(memoryTools) == 0 {
		return Usage{}
	}

	passMessages := append([]agentcontext.Message{}, messages...)
	passMessages = append(passMessages, agentcontext.Message{
		Role:    agentcontext.RoleUser,
		Content: memoryPassPrompt,
	})

	var usage Usage
	const maxMemoryPassIterations = 3

	for iteration := 1; iteration <= maxMemoryPassIterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return usage
		}

		logger.Debug("llm memory pass iteration start", logger.BuildMetadata("iteration", iteration))

		providerMessages := l.toProviderMessages(passMessages)
		result, err := l.provider.Stream(ctx, StreamParams{
			Messages: providerMessages,
			Tools:    memoryTools,
			Model:    l.model,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return usage
			}
			logger.Warning("llm memory pass stream error", logger.BuildMetadata("error", err))
			return usage
		}

		usage.PromptTokens += result.Usage.PromptTokens
		usage.CompletionTokens += result.Usage.CompletionTokens
		usage.TotalTokens += result.Usage.TotalTokens

		if len(result.Message.ToolCalls) == 0 {
			return usage
		}

		toolCalls := make([]agentcontext.ToolCall, 0, len(result.Message.ToolCalls))
		for _, tc := range result.Message.ToolCalls {
			toolCalls = append(toolCalls, agentcontext.ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			})
		}
		passMessages = append(passMessages, agentcontext.Message{
			Role:      agentcontext.RoleAssistantToolCalls,
			ToolCalls: toolCalls,
		})

		for _, tc := range result.Message.ToolCalls {
			if err := ctx.Err(); err != nil {
				return usage
			}

			var args json.RawMessage
			if tc.Arguments != "" {
				args = json.RawMessage(tc.Arguments)
			}

			toolResult, ok := executeTool(memoryTools, tc.Name, args)
			if !ok {
				toolResult = "error: tool not found"
			}
			passMessages = append(passMessages, agentcontext.Message{
				Role:       agentcontext.RoleTool,
				ToolCallID: tc.ID,
				Content:    toolResult,
			})
		}
	}

	return usage
}