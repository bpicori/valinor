package session

import (
	"encoding/json"
	"fmt"

	"valinor/internal/applog"
	agentcontext "valinor/internal/context"
)

// content structs for unmarshaling StoredEvent.Content by type
type userMessageContent struct {
	Content string `json:"content"`
}

type toolCallContent struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	ToolArgs   string `json:"tool_args"`
}

type toolResultContent struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	ToolResult string `json:"tool_result"`
}

type assistantContentPayload struct {
	Content string `json:"content"`
}

// ReconstructMessages converts stored events back to provider-agnostic context messages
// for session resume. The systemPrompt is prepended as the first message.
func ReconstructMessages(events []StoredEvent, systemPrompt string) ([]agentcontext.Message, error) {
	applog.Debug("session reconstruct start", applog.M("event_count", len(events)))
	if systemPrompt == "" {
		applog.Error("session reconstruct empty prompt", nil)
		return nil, fmt.Errorf("system prompt is required")
	}

	msgs := []agentcontext.Message{
		{
			Role:    agentcontext.RoleSystem,
			Content: systemPrompt,
		},
	}

	var pendingToolCalls []agentcontext.ToolCall

	for _, evt := range events {
		switch evt.Type {
		case StoredEventUserMessage:
			var c userMessageContent
			if err := json.Unmarshal(evt.Content, &c); err != nil {
				applog.Error("session reconstruct unmarshal user", applog.M("event_index", len(msgs), "error", err))
				return nil, fmt.Errorf("unmarshal user_message: %w", err)
			}
			msgs = append(msgs, agentcontext.Message{
				Role:    agentcontext.RoleUser,
				Content: c.Content,
			})

		case StoredEventToolCall:
			var c toolCallContent
			if err := json.Unmarshal(evt.Content, &c); err != nil {
				applog.Error("session reconstruct unmarshal tool call", applog.M("event_index", len(msgs), "error", err))
				return nil, fmt.Errorf("unmarshal tool_call: %w", err)
			}
			pendingToolCalls = append(pendingToolCalls, agentcontext.ToolCall{
				ID:        c.ToolCallID,
				Name:      c.ToolName,
				Arguments: c.ToolArgs,
			})

		case StoredEventToolResult:
			var c toolResultContent
			if err := json.Unmarshal(evt.Content, &c); err != nil {
				applog.Error("session reconstruct unmarshal tool result", applog.M("event_index", len(msgs), "error", err))
				return nil, fmt.Errorf("unmarshal tool_result: %w", err)
			}
			if len(pendingToolCalls) > 0 {
				msgs = append(msgs, agentcontext.Message{
					Role:      agentcontext.RoleAssistantToolCalls,
					ToolCalls: pendingToolCalls,
				})
				pendingToolCalls = nil
			}
			msgs = append(msgs, agentcontext.Message{
				Role:       agentcontext.RoleTool,
				ToolCallID: c.ToolCallID,
				Content:    c.ToolResult,
			})

		case StoredEventAssistantContent:
			if len(pendingToolCalls) > 0 {
				msgs = append(msgs, agentcontext.Message{
					Role:      agentcontext.RoleAssistantToolCalls,
					ToolCalls: pendingToolCalls,
				})
				pendingToolCalls = nil
			}
			var c assistantContentPayload
			if err := json.Unmarshal(evt.Content, &c); err != nil {
				applog.Error("session reconstruct unmarshal assistant", applog.M("event_index", len(msgs), "error", err))
				return nil, fmt.Errorf("unmarshal assistant_content: %w", err)
			}
			msgs = append(msgs, agentcontext.Message{
				Role:    agentcontext.RoleAssistant,
				Content: c.Content,
			})

		case StoredEventAgentThinking:
			// Telemetry-only for timeline persistence, skip reconstruction.
		case StoredEventAgentStepTokens:
			// Telemetry-only for timeline persistence, skip reconstruction.
		case StoredEventAgentDone:
			// Metadata only, skip
		}
	}

	if len(pendingToolCalls) > 0 {
		msgs = append(msgs, agentcontext.Message{
			Role:      agentcontext.RoleAssistantToolCalls,
			ToolCalls: pendingToolCalls,
		})
	}

	applog.Debug("session reconstruct success", applog.M("message_count", len(msgs), "event_count", len(events)))
	return msgs, nil
}
