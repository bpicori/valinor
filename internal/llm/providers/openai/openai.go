package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"valinor/internal/llm"
	"valinor/internal/logger"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Provider implements the OpenAI LLM provider.
type Provider struct {
	client     openai.Client
	apiKey     string
	baseURL    string
	httpClient *http.Client
	thinking   string
}

// New creates a new OpenAI provider instance.
func New(apiKey, baseURL string, httpClient *http.Client, thinking string) (*Provider, error) {
	opts := []option.RequestOption{
		option.WithHTTPClient(httpClient),
	}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Provider{
		client:     client,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: httpClient,
		thinking:   thinking,
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
	if params.MaxTokens > 0 {
		oaiParams.MaxTokens = openai.Int(int64(params.MaxTokens))
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
	var opts []option.RequestOption
	switch p.thinking {
	case "enabled":
		opts = append(opts, option.WithJSONSet("thinking.type", "enabled"))
	case "disabled":
		opts = append(opts, option.WithJSONSet("thinking.type", "disabled"))
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, oaiParams, opts...)
	acc := openai.ChatCompletionAccumulator{}

	var usage llm.Usage
	var assistantContent strings.Builder
	var reasoningContent strings.Builder

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

		// Handle content and reasoning deltas
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				assistantContent.WriteString(delta.Content)
				if params.OnDelta != nil {
					params.OnDelta(llm.StreamDelta{Content: delta.Content})
				}
			}
			if piece := reasoningDelta(delta); piece != "" {
				reasoningContent.WriteString(piece)
			}
			if delta.Refusal != "" {
				if params.OnDelta != nil {
					params.OnDelta(llm.StreamDelta{Refusal: delta.Refusal})
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		logger.Error("openai stream error", logger.BuildMetadata("error", err))
		return nil, err
	}

	// Build result message
	resultMsg := llm.Message{
		Role:             llm.RoleAssistant,
		Content:          assistantContent.String(),
		ReasoningContent: reasoningContent.String(),
	}
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
			resultMsg.ToolCalls = toolCalls
		}
	}

	return &llm.StreamResult{
		Message: resultMsg,
		Usage:   usage,
	}, nil
}

// reasoningDelta returns the reasoning_content fragment carried by a stream delta.
// DeepSeek returns this as an extension field that the OpenAI SDK does not model.
func reasoningDelta(delta openai.ChatCompletionChunkChoiceDelta) string {
	if raw := delta.RawJSON(); raw != "" {
		var payload struct {
			ReasoningContent string `json:"reasoning_content"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil && payload.ReasoningContent != "" {
			return payload.ReasoningContent
		}
	}
	if field, ok := delta.JSON.ExtraFields["reasoning_content"]; ok && field.Valid() {
		var piece string
		if err := json.Unmarshal([]byte(field.Raw()), &piece); err == nil {
			return piece
		}
	}
	return ""
}
