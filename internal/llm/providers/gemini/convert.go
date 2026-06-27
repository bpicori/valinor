package gemini

import (
	"encoding/json"

	"valinor/internal/llm"
	"valinor/internal/logger"
	"valinor/internal/tools"

	"google.golang.org/genai"
)

// ToGeminiContents converts provider-agnostic messages to Gemini Content format.
// Gemini uses RoleUser and RoleModel (not assistant), and system messages are treated as user messages.
func ToGeminiContents(messages []llm.Message, thoughtSignatures map[string][]byte) []*genai.Content {
	contents := []*genai.Content{}

	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			// Gemini doesn't have a system role - treat as user message
			contents = append(contents, genai.NewContentFromText(m.Content, genai.RoleUser))

		case llm.RoleUser:
			contents = append(contents, genai.NewContentFromText(m.Content, genai.RoleUser))

		case llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				// Assistant message with tool calls
				parts := []*genai.Part{}

				// Add text content if present
				if m.Content != "" {
					parts = append(parts, genai.NewPartFromText(m.Content))
				}

				// Add function call parts
				for _, tc := range m.ToolCalls {
					// Parse the arguments JSON
					var args map[string]any
					if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
						logger.Error("gemini tool call conversion error", logger.BuildMetadata("tool", tc.Name, "error", err))
						args = map[string]any{}
					}

					// Re-attach Gemini thought_signature when available.
					part := &genai.Part{
						FunctionCall: &genai.FunctionCall{
							ID:   tc.ID,
							Name: tc.Name,
							Args: args,
						},
					}
					if thoughtSignature, ok := thoughtSignatures[tc.ID]; ok {
						part.ThoughtSignature = thoughtSignature
					}
					parts = append(parts, part)
				}

				contents = append(contents, genai.NewContentFromParts(parts, genai.RoleModel))
			} else {
				// Regular assistant message
				contents = append(contents, genai.NewContentFromText(m.Content, genai.RoleModel))
			}

		case llm.RoleTool:
			// Tool results are sent as user messages with FunctionResponse
			var response map[string]any
			if err := json.Unmarshal([]byte(m.Content), &response); err != nil {
				// If it's not valid JSON, wrap it in a result field
				response = map[string]any{"result": m.Content}
			}

			contents = append(contents, genai.NewContentFromFunctionResponse(m.ToolCallID, response, genai.RoleUser))
		}
	}

	return contents
}

// ToGeminiFunctionDeclaration converts a provider-agnostic tool to Gemini format.
func ToGeminiFunctionDeclaration(t tools.Tool) *genai.FunctionDeclaration {
	// Parse the JSON Schema parameters
	var schema map[string]any
	if err := json.Unmarshal(t.Parameters, &schema); err != nil {
		logger.Error("gemini tool conversion error", logger.BuildMetadata("tool", t.Name, "error", err))
		schema = map[string]any{"type": "object"}
	}

	return &genai.FunctionDeclaration{
		Name:                 t.Name,
		Description:          t.Description,
		ParametersJsonSchema: schema,
	}
}
