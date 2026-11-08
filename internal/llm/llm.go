package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/event"
	"valinor/internal/logger"
	"valinor/internal/media"
	"valinor/internal/tools"
)

// New creates an LLM runtime with the given provider.
func New(provider Provider, model string, maxIterations, maxOutputTokens int) *LLM {
	if maxIterations <= 0 {
		maxIterations = 20
	}
	if maxOutputTokens <= 0 {
		maxOutputTokens = config.Must().Context.MaxOutputTokens
	}

	return &LLM{
		provider:        provider,
		model:           model,
		maxIterations:   maxIterations,
		maxOutputTokens: maxOutputTokens,
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

	callStream := func(msgs []agentcontext.Message, defs []tools.Tool) (*StreamResult, error) {
		return l.provider.Stream(ctx, StreamParams{
			Messages:  l.toProviderMessages(msgs),
			Tools:     defs,
			Model:     l.model,
			MaxTokens: l.maxOutputTokens,
			OnDelta: func(delta StreamDelta) {
				if delta.Content != "" {
					publishEvent(event.AgentContentDelta, event.ContentDeltaPayload{Content: delta.Content})
				}
				if delta.Refusal != "" {
					publishEvent(event.AgentRefusal, event.RefusalPayload{Content: delta.Refusal})
				}
			},
		})
	}

	priorTokens := req.PriorPromptTokens

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
			messages, compressionResult = req.Compressor.MaybeCompress(ctx, messages, priorTokens)
			if compressionResult.Applied {
				priorTokens = compressionResult.TokensAfter
			}
			publishCompressionResult(publishEvent, compressionResult)
		}

		// Build tool definitions from request tools
		toolDefs := req.Tools

		// Call provider stream with callback to publish deltas to the bus
		result, err := callStream(messages, toolDefs)

		// A provider rejecting the request for size means the local estimate was
		// low or the model window is smaller than configured: compress once and retry.
		if err != nil && req.Compressor != nil && isContextLengthError(err) {
			var forced CompressionResult
			messages, forced = req.Compressor.ForceCompress(ctx, messages, priorTokens)
			publishCompressionResult(publishEvent, forced)
			if forced.Applied {
				priorTokens = forced.TokensAfter
				logger.Warning("provider rejected context length; retrying after forced compression",
					logger.BuildMetadata("tokens_before", forced.TokensBefore, "tokens_after", forced.TokensAfter))
				result, err = callStream(messages, toolDefs)
			}
		}

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
				Role:             agentcontext.RoleAssistant,
				Content:          result.Message.Content,
				ReasoningContent: result.Message.ReasoningContent,
			})
			publishEvent(event.AgentDone, event.AgentDonePayload{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
			})
			logger.Debug("llm iteration done", logger.BuildMetadata("iteration", iterations, "usage", usage))

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
			Role:             agentcontext.RoleAssistantToolCalls,
			Content:          result.Message.Content,
			ReasoningContent: result.Message.ReasoningContent,
			ToolCalls:        toolCalls,
		})

		var toolImages []media.Image
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
				toolResult = tools.ToolResult{Text: "error: tool not found"}
			}
			logger.Debug("llm tool call done", logger.BuildMetadata("step", iterations, "tool_call_id", tc.ID, "tool", tc.Name, "found", ok, "result", toolResult.Text))
			publishEvent(event.AgentToolCallDone, event.ToolCallDonePayload{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolResult: toolResult.Text,
			})
			messages = append(messages, agentcontext.Message{
				Role:       agentcontext.RoleTool,
				ToolCallID: tc.ID,
				Content:    toolResult.Text,
			})
			toolImages = append(toolImages, toolResult.Images...)
		}

		if len(toolImages) > 0 {
			messages = append(messages, agentcontext.Message{
				Role:    agentcontext.RoleUser,
				Content: imageAttachmentMessage(toolImages),
				Images:  toolImages,
			})
		}
	}
}

// imageAttachmentMessage describes images that were attached after tool results.
// The image bytes reach the model through Message.Images; this text only gives
// the model the workspace paths.
func imageAttachmentMessage(images []media.Image) string {
	paths := make([]string, 0, len(images))
	for _, image := range images {
		if image.Path != "" {
			paths = append(paths, image.Path)
		}
	}
	if len(paths) == 0 {
		return "Attached image(s) are available for inspection."
	}
	return "Images attached for inspection: " + strings.Join(paths, ", ") + "."
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
				Images:  m.Images,
			})
		case agentcontext.RoleAssistant:
			out = append(out, Message{
				Role:             RoleAssistant,
				Content:          m.Content,
				ReasoningContent: m.ReasoningContent,
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
				Role:             RoleAssistant,
				Content:          m.Content,
				ReasoningContent: m.ReasoningContent,
				ToolCalls:        toolCalls,
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

func executeTool(toolset []tools.Tool, name string, args json.RawMessage) (tools.ToolResult, bool) {
	for _, t := range toolset {
		if t.Name != name {
			continue
		}
		return executeOne(t, args), true
	}
	return tools.ToolResult{}, false
}

// executeOne runs one tool and converts a panic into a tool result, so a buggy
// tool cannot take down the session or the process.
func executeOne(t tools.Tool, args json.RawMessage) (out tools.ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("tool panicked", logger.BuildMetadata("tool", t.Name, "panic", r, "stack", string(debug.Stack())))
			out = tools.ToolResult{Text: fmt.Sprintf("error: tool %s panicked: %v", t.Name, r)}
		}
	}()

	result, err := t.Exec(args)
	if err != nil {
		return tools.ToolResult{Text: fmt.Sprintf("error: %v", err)}
	}
	result.Text = tools.CapResult(t.Name, result.Text)
	return result
}

// isContextLengthError reports whether a provider error means the request
// exceeded the model context window.
func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"context length",
		"context_length",
		"maximum context",
		"context window",
		"too many tokens",
		"input is too long",
		"prompt is too long",
		"reduce the length",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
