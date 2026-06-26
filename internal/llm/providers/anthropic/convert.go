package anthropic

import (
	"encoding/json"

	"valinor/internal/llmapi"
	"valinor/internal/logger"
	"valinor/internal/toolschema"

	"github.com/anthropics/anthropic-sdk-go"
)

// ExtractSystemMessages extracts system messages from the message array.
// Anthropic requires system messages to be passed separately, not in the messages array.
func ExtractSystemMessages(messages []llmapi.Message) []anthropic.TextBlockParam {
	systemBlocks := []anthropic.TextBlockParam{}
	for _, m := range messages {
		if m.Role == llmapi.RoleSystem {
			systemBlocks = append(systemBlocks, anthropic.TextBlockParam{
				Text: m.Content,
			})
		}
	}
	return systemBlocks
}

// ToAnthropicMessages converts provider-agnostic messages to Anthropic format.
// Anthropic requires alternating user/assistant messages and does not include system messages
// in the messages array (they go in a separate System parameter).
func ToAnthropicMessages(messages []llmapi.Message) []anthropic.MessageParam {
	out := []anthropic.MessageParam{}

	for _, m := range messages {
		switch m.Role {
		case llmapi.RoleSystem:
			// Skip system messages - they're handled separately via ExtractSystemMessages
			continue

		case llmapi.RoleUser:
			out = append(out, anthropic.NewUserMessage(
				anthropic.NewTextBlock(m.Content),
			))

		case llmapi.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				// Assistant message with tool calls
				contentBlocks := []anthropic.ContentBlockParamUnion{}

				// Add text content if present
				if m.Content != "" {
					contentBlocks = append(contentBlocks, anthropic.NewTextBlock(m.Content))
				}

				// Add tool use blocks
				for _, tc := range m.ToolCalls {
					// Parse the arguments JSON
					var inputMap map[string]interface{}
					if err := json.Unmarshal([]byte(tc.Arguments), &inputMap); err != nil {
						logger.Error("anthropic tool call conversion error", logger.BuildMetadata("tool", tc.Name, "error", err))
						inputMap = map[string]interface{}{}
					}

					contentBlocks = append(contentBlocks, anthropic.NewToolUseBlock(
						tc.ID,
						inputMap, // input parameter accepts any type
						tc.Name,
					))
				}

				out = append(out, anthropic.NewAssistantMessage(contentBlocks...))
			} else {
				// Regular assistant message
				out = append(out, anthropic.NewAssistantMessage(
					anthropic.NewTextBlock(m.Content),
				))
			}

		case llmapi.RoleTool:
			// Tool results are sent as user messages with tool_result content blocks
			out = append(out, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(m.ToolCallID, m.Content, false),
			))
		}
	}

	return out
}

// ToAnthropicTool converts a provider-agnostic tool to Anthropic format.
func ToAnthropicTool(t toolschema.Tool) anthropic.ToolParam {
	// Parse the JSON Schema parameters
	var inputSchema map[string]interface{}
	if err := json.Unmarshal(t.Parameters, &inputSchema); err != nil {
		logger.Error("anthropic tool conversion error", logger.BuildMetadata("tool", t.Name, "error", err))
		inputSchema = map[string]interface{}{"type": "object"}
	}

	// Extract properties and required fields from the schema
	properties, _ := inputSchema["properties"]
	required, _ := inputSchema["required"].([]interface{})
	requiredStrings := []string{}
	for _, r := range required {
		if s, ok := r.(string); ok {
			requiredStrings = append(requiredStrings, s)
		}
	}

	return anthropic.ToolParam{
		Name:        t.Name,
		Description: anthropic.String(t.Description),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: properties,
			Required:   requiredStrings,
		},
	}
}
