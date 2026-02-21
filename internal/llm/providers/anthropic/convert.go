package anthropic

import (
	"encoding/json"

	"valinor/internal/applog"
	"valinor/internal/llm"

	"github.com/anthropics/anthropic-sdk-go"
)

// ExtractSystemMessages extracts system messages from the message array.
// Anthropic requires system messages to be passed separately, not in the messages array.
func ExtractSystemMessages(messages []llm.Message) []anthropic.TextBlockParam {
	systemBlocks := []anthropic.TextBlockParam{}
	for _, m := range messages {
		if m.Role == llm.RoleSystem {
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
func ToAnthropicMessages(messages []llm.Message) []anthropic.MessageParam {
	out := []anthropic.MessageParam{}

	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			// Skip system messages - they're handled separately via ExtractSystemMessages
			continue

		case llm.RoleUser:
			out = append(out, anthropic.NewUserMessage(
				anthropic.NewTextBlock(m.Content),
			))

		case llm.RoleAssistant:
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
						applog.Error("anthropic tool call conversion error", applog.M("tool", tc.Name, "error", err))
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

		case llm.RoleTool:
			// Tool results are sent as user messages with tool_result content blocks
			out = append(out, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(m.ToolCallID, m.Content, false),
			))
		}
	}

	return out
}

// ToAnthropicTool converts a provider-agnostic tool definition to Anthropic format.
func ToAnthropicTool(tool llm.ToolDefinition) anthropic.ToolParam {
	// Parse the JSON Schema parameters
	var inputSchema map[string]interface{}
	if err := json.Unmarshal(tool.Parameters, &inputSchema); err != nil {
		applog.Error("anthropic tool conversion error", applog.M("tool", tool.Name, "error", err))
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
		Name:        tool.Name,
		Description: anthropic.String(tool.Description),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: properties,
			Required:   requiredStrings,
		},
	}
}
