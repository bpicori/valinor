package anthropic

import (
	"context"
	"fmt"

	"valinor/internal/llmapi"
	"valinor/internal/logger"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Provider implements the Anthropic (Claude) LLM provider.
type Provider struct {
	client anthropic.Client
}

// New creates a new Anthropic provider instance.
func New(apiKey string) (*Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required for Anthropic provider")
	}

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	return &Provider{
		client: client,
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "anthropic"
}

// ListModels returns models available from the Anthropic API.
func (p *Provider) ListModels(ctx context.Context) ([]llmapi.ModelInfo, error) {
	iter := p.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	var out []llmapi.ModelInfo
	for iter.Next() {
		m := iter.Current()
		if m.ID == "" {
			continue
		}
		out = append(out, llmapi.ModelInfo{
			ID:          m.ID,
			DisplayName: m.DisplayName,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Stream implements the Provider interface for Anthropic.
func (p *Provider) Stream(ctx context.Context, params llmapi.StreamParams) (*llmapi.StreamResult, error) {
	// Extract system messages (Anthropic requires these separately)
	systemMessages := ExtractSystemMessages(params.Messages)

	// Convert messages to Anthropic format
	// Anthropic requires a positive max_tokens value; default to 16384 if unset
	maxTokens := int64(params.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = 16384
	}
	antParams := anthropic.MessageNewParams{
		Model:     anthropic.Model(params.Model),
		MaxTokens: maxTokens,
		Messages:  ToAnthropicMessages(params.Messages),
		System:    systemMessages,
	}

	// Add tools if provided
	if len(params.Tools) > 0 {
		toolDefs := make([]anthropic.ToolUnionParam, len(params.Tools))
		for i := range params.Tools {
			converted := ToAnthropicTool(params.Tools[i])
			toolDefs[i] = anthropic.ToolUnionParam{
				OfTool: &converted,
			}
		}
		antParams.Tools = toolDefs
	}

	// Start streaming
	stream := p.client.Messages.NewStreaming(ctx, antParams)

	var usage llmapi.Usage
	var assistantContent string
	contentBlocks := []anthropic.ContentBlockUnion{}

	for stream.Next() {
		evt := stream.Current()

		switch evt.Type {
		case "message_delta":
			// Message delta contains usage information
			msgDelta := evt.AsMessageDelta()
			usage = llmapi.Usage{
				PromptTokens:     int64(msgDelta.Usage.InputTokens),
				CompletionTokens: int64(msgDelta.Usage.OutputTokens),
				TotalTokens:      int64(msgDelta.Usage.InputTokens + msgDelta.Usage.OutputTokens),
			}

		case "content_block_start":
			// New content block starting - store tool use blocks
			blockStart := evt.AsContentBlockStart()
			// Store the content block (we'll update it with deltas later)
			if blockStart.ContentBlock.Type == "tool_use" {
				// Initialize with empty input - will be filled by deltas
				contentBlocks = append(contentBlocks, anthropic.ContentBlockUnion{
					Type:  blockStart.ContentBlock.Type,
					ID:    blockStart.ContentBlock.ID,
					Name:  blockStart.ContentBlock.Name,
					Input: []byte(""),
				})
			}

		case "content_block_delta":
			// Content delta - accumulate text
			blockDelta := evt.AsContentBlockDelta()
			switch blockDelta.Delta.Type {
			case "text_delta":
				if blockDelta.Delta.Text != "" {
					assistantContent += blockDelta.Delta.Text
					if params.OnDelta != nil {
						params.OnDelta(llmapi.StreamDelta{Content: blockDelta.Delta.Text})
					}
				}
			case "input_json_delta":
				// Tool input is being streamed - accumulate to the last content block
				if len(contentBlocks) > 0 {
					idx := len(contentBlocks) - 1
					contentBlocks[idx].Input = append(contentBlocks[idx].Input, []byte(blockDelta.Delta.PartialJSON)...)
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		logger.Error("anthropic stream error", logger.BuildMetadata("error", err))
		return nil, err
	}

	// Build result message
	var resultMsg llmapi.Message

	if len(contentBlocks) > 0 {
		// Assistant requested tool calls
		toolCalls := []llmapi.ToolCall{}
		for _, block := range contentBlocks {
			if block.Type == "tool_use" {
				toolCalls = append(toolCalls, llmapi.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Arguments: string(block.Input),
				})
			}
		}
		resultMsg = llmapi.Message{
			Role:      llmapi.RoleAssistant,
			ToolCalls: toolCalls,
		}
	} else {
		// Regular assistant message
		resultMsg = llmapi.Message{
			Role:    llmapi.RoleAssistant,
			Content: assistantContent,
		}
	}

	return &llmapi.StreamResult{
		Message: resultMsg,
		Usage:   usage,
	}, nil
}
