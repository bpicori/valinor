package openai

import (
	"encoding/json"

	"valinor/internal/llm"
	"valinor/internal/logger"
	"valinor/internal/tools"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"github.com/openai/openai-go/v3/shared/constant"
)

// ToOpenAIMessages converts provider-agnostic messages to OpenAI format.
func ToOpenAIMessages(messages []llm.Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case llm.RoleUser:
			out = append(out, openai.UserMessage(m.Content))
		case llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				// Assistant message with tool calls
				toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID:   tc.ID,
							Type: constant.Function("function"),
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Name,
								Arguments: tc.Arguments,
							},
						},
					})
				}
				out = append(out, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Role:      constant.Assistant("assistant"),
						ToolCalls: toolCalls,
					},
				})
			} else {
				// Regular assistant message
				out = append(out, openai.AssistantMessage(m.Content))
			}
		case llm.RoleTool:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return out
}

// ToOpenAITool converts a provider-agnostic tool to OpenAI format.
func ToOpenAITool(t tools.Tool) openai.ChatCompletionToolUnionParam {
	// Parse the JSON Schema parameters
	var params shared.FunctionParameters
	if err := json.Unmarshal(t.Parameters, &params); err != nil {
		logger.Error("openai tool conversion error", logger.BuildMetadata("tool", t.Name, "error", err))
		params = shared.FunctionParameters{"type": "object"}
	}

	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        t.Name,
		Description: openai.String(t.Description),
		Parameters:  params,
	})
}
