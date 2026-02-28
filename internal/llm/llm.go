package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentcontext "valinor/internal/context"
	"valinor/internal/event"
	"valinor/internal/event_bus"
	"valinor/internal/logger"
)

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// New creates an LLM runtime with the given provider and tools.
func New(provider Provider, model string, tools []Tool, b *event_bus.Bus, maxIterations int) (*LLM, error) {
	if maxIterations <= 0 {
		maxIterations = 20
	}

	return &LLM{
		provider:      provider,
		model:         model,
		bus:           b,
		maxIterations: maxIterations,
		tools:         tools,
	}, nil
}

// SetModel updates the model used for subsequent runs.
func (l *LLM) SetModel(model string) {
	l.model = model
}

// Tools returns the tools attached to this LLM (for agent rebuild).
func (l *LLM) Tools() []Tool {
	return l.tools
}

// MaxIterations returns the max iterations cap.
func (l *LLM) MaxIterations() int {
	return l.maxIterations
}

// Run executes one LLM operation: reset, restore, or user-message turn.
func (l *LLM) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	messages := append([]agentcontext.Message{}, req.Messages...)
	if len(messages) == 0 {
		return RunResult{}, nil
	}

	var usage Usage
	iterations := 0

	// Helper to create events with session context
	newEvent := func(eventType event.EventType, payload any) event.Event {
		return event.Event{
			Type:      eventType,
			Payload:   payload,
			SessionID: req.SessionID,
			RequestID: req.RequestID,
		}
	}

	for {
		// Check for cancellation before each iteration
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				logger.Info("llm run cancelled by user", nil)
				l.bus.Publish(newEvent(event.AgentCancelled, event.AgentCancelledPayload{}))
				return RunResult{Messages: messages}, nil
			}
			return RunResult{}, err
		}

		if iterations >= l.maxIterations {
			l.bus.Publish(newEvent(event.AgentError, event.AgentErrorPayload{
				Error: fmt.Errorf("max agent iterations (%d) reached; stopping to prevent runaway loop", l.maxIterations),
			}))
			return RunResult{}, fmt.Errorf("max agent iterations (%d) reached", l.maxIterations)
		}
		iterations++

		logger.Debug("llm iteration start", logger.BuildMetadata("iteration", iterations, "provider", l.provider.Name()))

		// Convert to provider-agnostic messages
		providerMessages := l.toProviderMessages(messages)

		// Build tool definitions
		toolDefs := make([]ToolDefinition, len(l.tools))
		for i := range l.tools {
			toolDefs[i] = l.tools[i].Def
		}

		// Call provider stream with callback to publish deltas to the bus
		result, err := l.provider.Stream(ctx, StreamParams{
			Messages: providerMessages,
			Tools:    toolDefs,
			Model:    l.model,
			OnDelta: func(delta StreamDelta) {
				if delta.Content != "" {
					l.bus.Publish(newEvent(event.AgentContentDelta, event.ContentDeltaPayload{Content: delta.Content}))
				}
				if delta.Refusal != "" {
					l.bus.Publish(newEvent(event.AgentRefusal, event.RefusalPayload{Content: delta.Refusal}))
				}
			},
		})
		if err != nil {
			// Handle user cancellation gracefully
			if errors.Is(err, context.Canceled) {
				logger.Info("llm stream cancelled by user", nil)
				l.bus.Publish(newEvent(event.AgentCancelled, event.AgentCancelledPayload{}))
				return RunResult{Messages: messages}, nil
			}
			logger.Error("llm stream error", logger.BuildMetadata("error", err))
			l.bus.Publish(newEvent(event.AgentError, event.AgentErrorPayload{Error: err}))
			return RunResult{}, err
		}

		// Update usage stats
		usage = result.Usage
		l.bus.Publish(newEvent(event.AgentStepTokens, event.StepTokenUsagePayload{
			Step:             iterations,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.TotalTokens,
		}))

		// Publish full content if present
		if result.Message.Content != "" {
			l.bus.Publish(newEvent(event.AgentContentFull, event.ContentFullPayload{Content: result.Message.Content}))
		}

		// Handle result message
		if len(result.Message.ToolCalls) == 0 {
			// No tool calls - we're done
			messages = append(messages, agentcontext.Message{
				Role:    agentcontext.RoleAssistant,
				Content: result.Message.Content,
			})
			l.bus.Publish(newEvent(event.AgentDone, event.AgentDonePayload{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
			}))
			logger.Debug("llm iteration done", logger.BuildMetadata("iteration", iterations))
			return RunResult{Messages: messages}, nil
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

		// Execute tool calls
		for _, tc := range result.Message.ToolCalls {
			var args json.RawMessage
			if tc.Arguments != "" {
				args = json.RawMessage(tc.Arguments)
			}
			logger.Debug("llm tool call start", logger.BuildMetadata("step", iterations, "tool_call_id", tc.ID, "tool", tc.Name, "args", string(args)))
			l.bus.Publish(newEvent(event.AgentThinking, event.ThinkingPayload{
				Step:    iterations,
				Content: "executing tool: " + tc.Name,
			}))
			l.bus.Publish(newEvent(event.AgentToolCallStart, event.ToolCallStartPayload{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolArgs:   string(args),
			}))
			toolResult, ok := l.executeTool(tc.Name, args)
			if !ok {
				toolResult = "error: tool not found"
			}
			logger.Debug("llm tool call done", logger.BuildMetadata("step", iterations, "tool_call_id", tc.ID, "tool", tc.Name, "found", ok, "result", toolResult))
			l.bus.Publish(newEvent(event.AgentToolCallDone, event.ToolCallDonePayload{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolResult: toolResult,
			}))
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

func (l *LLM) executeTool(toolName string, args json.RawMessage) (string, bool) {
	for _, t := range l.tools {
		if t.Name != toolName {
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
