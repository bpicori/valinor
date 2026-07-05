package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentcontext "valinor/internal/context"
	"valinor/internal/event"
	"valinor/internal/logger"
	"valinor/internal/tools"
)

// New creates an LLM runtime with the given provider.
func New(provider Provider, model string, maxIterations int) *LLM {
	if maxIterations <= 0 {
		maxIterations = 20
	}

	return &LLM{
		provider:      provider,
		model:         model,
		maxIterations: maxIterations,
	}
}

// Run executes one LLM operation: reset, restore, or user-message turn.
func (l *LLM) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	messages := append([]agentcontext.Message{}, req.Messages...)
	if len(messages) == 0 {
		return RunResult{}, nil
	}

	var usage Usage
	iterations := 0
	toolsUsedThisRun := make(map[string]struct{})

	// Helper to create events with session context
	newEvent := func(eventType event.EventType, payload any) event.Event {
		return event.Event{
			Type:      eventType,
			Payload:   payload,
			SessionID: req.SessionID,
			RequestID: req.RequestID,
		}
	}
	publishEvent := func(eventType event.EventType, payload any) {
		if !req.EmitEvents {
			return
		}
		event.Publish(newEvent(eventType, payload))
	}

	for {
		// Check for cancellation before each iteration
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				logger.Info("llm run cancelled by user", nil)
				publishEvent(event.AgentCancelled, event.AgentCancelledPayload{})
				return RunResult{Messages: messages, Usage: usage}, nil
			}
			return RunResult{}, err
		}

		if iterations >= l.maxIterations {
			return l.runGracefulFinalTurn(ctx, req, messages, usage, toolsUsedThisRun, publishEvent)
		}
		iterations++

		logger.Debug("llm iteration start", logger.BuildMetadata("iteration", iterations, "provider", l.provider.Name()))

		if warn := iterationBudgetWarning(iterations, l.maxIterations); warn != "" {
			messages = append(messages, agentcontext.Message{
				Role:    agentcontext.RoleUser,
				Content: warn,
			})
		}

		if req.Compressor != nil {
			var compressionResult CompressionResult
			messages, compressionResult = req.Compressor.MaybeCompress(ctx, messages)
			publishCompressionResult(publishEvent, compressionResult)
		}

		// Convert to provider-agnostic messages
		providerMessages := l.toProviderMessages(messages)

		// Build tool definitions from request tools
		toolDefs := req.Tools

		// Call provider stream with callback to publish deltas to the bus
		result, err := l.provider.Stream(ctx, StreamParams{
			Messages: providerMessages,
			Tools:    toolDefs,
			Model:    l.model,
			OnDelta: func(delta StreamDelta) {
				if delta.Content != "" {
					publishEvent(event.AgentContentDelta, event.ContentDeltaPayload{Content: delta.Content})
				}
				if delta.Refusal != "" {
					publishEvent(event.AgentRefusal, event.RefusalPayload{Content: delta.Refusal})
				}
			},
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				logger.Info("llm stream cancelled by user", nil)
				publishEvent(event.AgentCancelled, event.AgentCancelledPayload{})
				return RunResult{Messages: messages, Usage: usage}, nil
			}
			logger.Error("llm stream error", logger.BuildMetadata("error", err))
			publishEvent(event.AgentError, event.AgentErrorPayload{Error: err})
			return RunResult{}, err
		}

		// Update usage stats
		usage = result.Usage
		publishEvent(event.AgentStepTokens, event.StepTokenUsagePayload{
			Step:             iterations,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.TotalTokens,
		})

		// Publish full content if present
		if result.Message.Content != "" {
			publishEvent(event.AgentContentFull, event.ContentFullPayload{Content: result.Message.Content})
		}

		// Handle result message
		if len(result.Message.ToolCalls) == 0 {
			messages = append(messages, agentcontext.Message{
				Role:    agentcontext.RoleAssistant,
				Content: result.Message.Content,
			})
			publishEvent(event.AgentDone, event.AgentDonePayload{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
			})
			logger.Debug("llm iteration done", logger.BuildMetadata("iteration", iterations, "usage", usage))

			if shouldRunMemoryPass(toolsUsedThisRun) {
				memoryUsage := l.runSilentMemoryPass(ctx, req, messages)
				usage.PromptTokens += memoryUsage.PromptTokens
				usage.CompletionTokens += memoryUsage.CompletionTokens
				usage.TotalTokens += memoryUsage.TotalTokens
			}

			return RunResult{Messages: messages, Usage: usage}, nil
		}

		// Handle tool calls
		toolCalls := make([]agentcontext.ToolCall, 0, len(result.Message.ToolCalls))
		for _, tc := range result.Message.ToolCalls {
			toolCalls = append(toolCalls, agentcontext.ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			})
		}
		messages = append(messages, agentcontext.Message{
			Role:      agentcontext.RoleAssistantToolCalls,
			ToolCalls: toolCalls,
		})

		for _, tc := range result.Message.ToolCalls {
			if err := ctx.Err(); err != nil {
				if errors.Is(err, context.Canceled) {
					logger.Info("llm run cancelled by user", nil)
					publishEvent(event.AgentCancelled, event.AgentCancelledPayload{})
					return RunResult{Messages: messages, Usage: usage}, nil
				}
				return RunResult{}, err
			}

			var args json.RawMessage
			if tc.Arguments != "" {
				args = json.RawMessage(tc.Arguments)
			}
			logger.Debug("llm tool call start", logger.BuildMetadata("step", iterations, "tool_call_id", tc.ID, "tool", tc.Name, "args", string(args)))
			publishEvent(event.AgentThinking, event.ThinkingPayload{
				Step:    iterations,
				Content: "executing tool: " + tc.Name,
			})
			publishEvent(event.AgentToolCallStart, event.ToolCallStartPayload{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolArgs:   string(args),
			})

			toolsUsedThisRun[tc.Name] = struct{}{}

			toolResult, ok := executeTool(req.Tools, tc.Name, args)
			if !ok {
				toolResult = "error: tool not found"
			}
			logger.Debug("llm tool call done", logger.BuildMetadata("step", iterations, "tool_call_id", tc.ID, "tool", tc.Name, "found", ok, "result", toolResult))
			publishEvent(event.AgentToolCallDone, event.ToolCallDonePayload{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolResult: toolResult,
			})
			messages = append(messages, agentcontext.Message{
				Role:       agentcontext.RoleTool,
				ToolCallID: tc.ID,
				Content:    toolResult,
			})
		}
	}
}

// toProviderMessages converts internal context messages to provider-agnostic format.
func (l *LLM) toProviderMessages(messages []agentcontext.Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case agentcontext.RoleSystem:
			out = append(out, Message{
				Role:    RoleSystem,
				Content: m.Content,
			})
		case agentcontext.RoleUser:
			out = append(out, Message{
				Role:    RoleUser,
				Content: m.Content,
			})
		case agentcontext.RoleAssistant:
			out = append(out, Message{
				Role:    RoleAssistant,
				Content: m.Content,
			})
		case agentcontext.RoleAssistantToolCalls:
			toolCalls := make([]ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				toolCalls = append(toolCalls, ToolCall{
					ID:        tc.ID,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				})
			}
			out = append(out, Message{
				Role:      RoleAssistant,
				ToolCalls: toolCalls,
			})
		case agentcontext.RoleTool:
			out = append(out, Message{
				Role:       RoleTool,
				ToolCallID: m.ToolCallID,
				Content:    m.Content,
			})
		}
	}
	return out
}

func executeTool(toolset []tools.Tool, name string, args json.RawMessage) (string, bool) {
	for _, t := range toolset {
		if t.Name != name {
			continue
		}
		result, err := t.Exec(args)
		if err != nil {
			return fmt.Sprintf("error: %v", err), true
		}
		return result, true
	}
	return "", false
}


