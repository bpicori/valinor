package openai

import (
	"encoding/json"

	"valinor/internal/llm"
	"valinor/internal/logger"
	"valinor/internal/tools"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
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
			if len(m.Images) == 0 {
				out = append(out, openai.UserMessage(m.Content))
				continue
			}

			parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, openai.TextContentPart(m.Content))
			}
			for _, image := range m.Images {
				if len(image.Data) == 0 {
					continue
				}
				parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
					URL: image.DataURI(),
				}))
			}
			if len(parts) == 0 {
				out = append(out, openai.UserMessage(m.Content))
				continue
			}
			out = append(out, openai.UserMessage(parts))
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
				assistant := &openai.ChatCompletionAssistantMessageParam{
					Role:      constant.Assistant("assistant"),
					ToolCalls: toolCalls,
				}
				if m.Content != "" {
					assistant.Content.OfString = param.NewOpt(m.Content)
				}
				if m.ReasoningContent != "" {
					assistant.SetExtraFields(map[string]any{
						"reasoning_content": m.ReasoningContent,
					})
				}
				out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: assistant})
			} else {
				// Regular assistant message
				assistant := openai.AssistantMessage(m.Content)
				if m.ReasoningContent != "" {
					assistant.OfAssistant.SetExtraFields(map[string]any{
						"reasoning_content": m.ReasoningContent,
					})
				}
				out = append(out, assistant)
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
