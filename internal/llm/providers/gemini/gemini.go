package gemini

import (
	"context"
	"encoding/json"
	"fmt"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/llm"

	"google.golang.org/genai"
)

// Provider implements the Gemini LLM provider.
type Provider struct {
	client *genai.Client
	bus    *bus.Bus
}

// New creates a new Gemini provider instance.
func New(apiKey string, b *bus.Bus) (*Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required for Gemini provider")
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &Provider{
		client: client,
		bus:    b,
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "gemini"
}

// Stream implements the Provider interface for Gemini.
func (p *Provider) Stream(ctx context.Context, params llm.StreamParams) (*llm.StreamResult, error) {
	// Convert messages to Gemini format
	contents := ToGeminiContents(params.Messages)

	// Prepare config
	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(params.MaxTokens),
	}

	// Add tools if provided
	if len(params.Tools) > 0 {
		functionDecls := make([]*genai.FunctionDeclaration, len(params.Tools))
		for i := range params.Tools {
			functionDecls[i] = ToGeminiFunctionDeclaration(params.Tools[i])
		}
		config.Tools = []*genai.Tool{
			{FunctionDeclarations: functionDecls},
		}
	}

	// Start streaming using iterator pattern
	stream := p.client.Models.GenerateContentStream(ctx, params.Model, contents, config)

	var usage llm.Usage
	var assistantContent string
	var functionCalls []*genai.FunctionCall
	callIDCounter := 0 // Gemini doesn't provide IDs, so we generate sequential ones

	for response, err := range stream {
		if err != nil {
			applog.Error("gemini stream error", applog.M("error", err))
			return nil, err
		}

		// Extract usage metadata
		if response.UsageMetadata != nil {
			usage = llm.Usage{
				PromptTokens:     int64(response.UsageMetadata.PromptTokenCount),
				CompletionTokens: int64(response.UsageMetadata.CandidatesTokenCount),
				TotalTokens:      int64(response.UsageMetadata.TotalTokenCount),
			}
		}

		// Process candidates
		if len(response.Candidates) > 0 {
			candidate := response.Candidates[0]
			if candidate.Content != nil {
				for _, part := range candidate.Content.Parts {
					// Handle text parts
					if part.Text != "" {
						assistantContent += part.Text
						p.bus.Publish(bus.Event{
							Type:    bus.EventAgentContentDelta,
							Payload: bus.ContentDeltaPayload{Content: part.Text},
						})
					}

					// Handle function calls
					if part.FunctionCall != nil {
						// Generate an ID if not provided
						if part.FunctionCall.ID == "" {
							part.FunctionCall.ID = fmt.Sprintf("call_%d", callIDCounter)
							callIDCounter++
						}
						functionCalls = append(functionCalls, part.FunctionCall)
					}
				}
			}
		}
	}

	// Build result message
	var resultMsg llm.Message

	if len(functionCalls) > 0 {
		// Assistant requested tool calls
		toolCalls := []llm.ToolCall{}
		for _, fc := range functionCalls {
			// Marshal the arguments to JSON string
			argsJSON, err := json.Marshal(fc.Args)
			if err != nil {
				applog.Error("gemini function call args marshal error", applog.M("function", fc.Name, "error", err))
				argsJSON = []byte("{}")
			}

			toolCalls = append(toolCalls, llm.ToolCall{
				ID:        fc.ID,
				Name:      fc.Name,
				Arguments: string(argsJSON),
			})
		}
		resultMsg = llm.Message{
			Role:      llm.RoleAssistant,
			ToolCalls: toolCalls,
		}
	} else {
		// Regular assistant message
		resultMsg = llm.Message{
			Role:    llm.RoleAssistant,
			Content: assistantContent,
		}
	}

	return &llm.StreamResult{
		Message: resultMsg,
		Usage:   usage,
	}, nil
}
