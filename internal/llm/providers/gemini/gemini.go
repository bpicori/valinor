package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"valinor/internal/llm"
	"valinor/internal/logger"

	"google.golang.org/genai"
)

// Provider implements the Gemini LLM provider.
type Provider struct {
	client *genai.Client

	mu                sync.RWMutex
	thoughtSignatures map[string][]byte
}

// New creates a new Gemini provider instance.
func New(apiKey string, httpClient *http.Client) (*Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required for Gemini provider")
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &Provider{
		client:            client,
		thoughtSignatures: make(map[string][]byte),
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "gemini"
}

// ListModels returns models available from the Gemini API.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	var out []llm.ModelInfo
	for m, err := range p.client.Models.All(ctx) {
		if err != nil {
			return nil, err
		}
		id := m.Name
		if id == "" {
			continue
		}
		// Strip "models/" prefix for consistency with Stream API usage
		if len(id) > 7 && id[:7] == "models/" {
			id = id[7:]
		}
		out = append(out, llm.ModelInfo{
			ID:          id,
			DisplayName: m.DisplayName,
		})
	}
	return out, nil
}

// Stream implements the Provider interface for Gemini.
func (p *Provider) Stream(ctx context.Context, params llm.StreamParams) (*llm.StreamResult, error) {
	// Convert messages to Gemini format
	contents := ToGeminiContents(params.Messages, p.snapshotThoughtSignatures())

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
	var toolCalls []llm.ToolCall
	callIDCounter := 0

	for response, err := range stream {
		if err != nil {
			logger.Error("gemini stream error", logger.BuildMetadata("error", err))
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
						if params.OnDelta != nil {
							params.OnDelta(llm.StreamDelta{Content: part.Text})
						}
					}

					// Handle function calls
					if part.FunctionCall != nil {
						// Generate an ID if not provided
						id := part.FunctionCall.ID
						if id == "" {
							id = fmt.Sprintf("call_%d", callIDCounter)
							callIDCounter++
						}

						// Marshal the arguments to JSON string
						argsJSON, err := json.Marshal(part.FunctionCall.Args)
						if err != nil {
							logger.Error("gemini function call args marshal error", logger.BuildMetadata("function", part.FunctionCall.Name, "error", err))
							argsJSON = []byte("{}")
						}

						toolCalls = append(toolCalls, llm.ToolCall{
							ID:        id,
							Name:      part.FunctionCall.Name,
							Arguments: string(argsJSON),
						})
						p.storeThoughtSignature(id, part.ThoughtSignature)
					}
				}
			}
		}
	}

	// Build result message
	var resultMsg llm.Message

	if len(toolCalls) > 0 {
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

func (p *Provider) storeThoughtSignature(toolCallID string, signature []byte) {
	if toolCallID == "" || len(signature) == 0 {
		return
	}
	dup := append([]byte(nil), signature...)
	p.mu.Lock()
	p.thoughtSignatures[toolCallID] = dup
	p.mu.Unlock()
}

func (p *Provider) snapshotThoughtSignatures() map[string][]byte {
	p.mu.RLock()
	defer p.mu.RUnlock()

	snapshot := make(map[string][]byte, len(p.thoughtSignatures))
	for id, signature := range p.thoughtSignatures {
		snapshot[id] = append([]byte(nil), signature...)
	}
	return snapshot
}
