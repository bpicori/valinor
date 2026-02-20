package session

import (
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared/constant"
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

// ReconstructMessages converts stored events back to OpenAI message params
// for session resume. The systemPrompt is prepended as the first message.
func ReconstructMessages(events []StoredEvent, systemPrompt string) ([]openai.ChatCompletionMessageParamUnion, error) {
	if systemPrompt == "" {
		return nil, fmt.Errorf("system prompt is required")
	}

	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
	}

	var pendingToolCalls []openai.ChatCompletionMessageToolCallUnionParam

	for _, evt := range events {
		switch evt.Type {
		case StoredEventUserMessage:
			var c userMessageContent
			if err := json.Unmarshal(evt.Content, &c); err != nil {
				return nil, fmt.Errorf("unmarshal user_message: %w", err)
			}
			msgs = append(msgs, openai.UserMessage(c.Content))

		case StoredEventToolCall:
			var c toolCallContent
			if err := json.Unmarshal(evt.Content, &c); err != nil {
				return nil, fmt.Errorf("unmarshal tool_call: %w", err)
			}
			pendingToolCalls = append(pendingToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID:   c.ToolCallID,
					Type: constant.Function("function"),
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      c.ToolName,
						Arguments: c.ToolArgs,
					},
				},
			})

		case StoredEventToolResult:
			var c toolResultContent
			if err := json.Unmarshal(evt.Content, &c); err != nil {
				return nil, fmt.Errorf("unmarshal tool_result: %w", err)
			}
			if len(pendingToolCalls) > 0 {
				msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role:      constant.Assistant("assistant"),
						ToolCalls: pendingToolCalls,
					},
				})
				pendingToolCalls = nil
			}
			msgs = append(msgs, openai.ToolMessage(c.ToolResult, c.ToolCallID))

		case StoredEventAssistantContent:
			if len(pendingToolCalls) > 0 {
				msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role:      constant.Assistant("assistant"),
						ToolCalls: pendingToolCalls,
					},
				})
				pendingToolCalls = nil
			}
			var c assistantContentPayload
			if err := json.Unmarshal(evt.Content, &c); err != nil {
				return nil, fmt.Errorf("unmarshal assistant_content: %w", err)
			}
			msgs = append(msgs, openai.AssistantMessage(c.Content))

		case StoredEventAgentDone:
			// Metadata only, skip
		}
	}

	if len(pendingToolCalls) > 0 {
		msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				Role:      constant.Assistant("assistant"),
				ToolCalls: pendingToolCalls,
			},
		})
	}

	return msgs, nil
}
