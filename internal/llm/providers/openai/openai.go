package openai

import (
	"context"

	"valinor/internal/applog"
	"valinor/internal/llm"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Provider implements the OpenAI LLM provider.
type Provider struct {
	client openai.Client
}

// New creates a new OpenAI provider instance.
func New(apiKey, baseURL string) (*Provider, error) {
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)
	return &Provider{
		client: client,
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "openai"
}

// ListModels returns models available from the OpenAI API.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	iter := p.client.Models.ListAutoPaging(ctx)
	var out []llm.ModelInfo
	for iter.Next() {
		m := iter.Current()
		if m.ID == "" {
			continue
		}
		out = append(out, llm.ModelInfo{
			ID:          m.ID,
			DisplayName: m.ID,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Stream implements the Provider interface for OpenAI.
func (p *Provider) Stream(ctx context.Context, params llm.StreamParams) (*llm.StreamResult, error) {
	oaiParams := openai.ChatCompletionNewParams{
		Model:    params.Model,
		Messages: ToOpenAIMessages(params.Messages),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}

	// Add tools if provided
	if len(params.Tools) > 0 {
		toolDefs := make([]openai.ChatCompletionToolUnionParam, len(params.Tools))
		for i := range params.Tools {
			toolDefs[i] = ToOpenAITool(params.Tools[i])
		}
		oaiParams.Tools = toolDefs
	}

	// Start streaming
	stream := p.client.Chat.Completions.NewStreaming(ctx, oaiParams)
	acc := openai.ChatCompletionAccumulator{}

	var usage llm.Usage
	var assistantContent string

	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)

		// Handle token usage
		if chunk.Usage.TotalTokens > 0 {
			usage = llm.Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}

		// Handle content deltas
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				assistantContent += delta.Content
				if params.OnDelta != nil {
					params.OnDelta(llm.StreamDelta{Content: delta.Content})
				}
			}
			if delta.Refusal != "" {
				if params.OnDelta != nil {
					params.OnDelta(llm.StreamDelta{Refusal: delta.Refusal})
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		applog.Error("openai stream error", applog.BuildMetadata("error", err))
		return nil, err
	}

	// Build result message
	var resultMsg llm.Message
	if len(acc.Choices) > 0 {
		msg := acc.Choices[0].Message
		if len(msg.ToolCalls) > 0 {
			// Assistant requested tool calls
			toolCalls := make([]llm.ToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				toolCalls = append(toolCalls, llm.ToolCall{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
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
	}

	return &llm.StreamResult{
		Message: resultMsg,
		Usage:   usage,
	}, nil
}
