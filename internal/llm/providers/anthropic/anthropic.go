package anthropic

import (
	"context"
	"fmt"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/llm"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Provider implements the Anthropic (Claude) LLM provider.
type Provider struct {
	client anthropic.Client
	bus    *bus.Bus
}

// New creates a new Anthropic provider instance.
func New(apiKey string, b *bus.Bus) (*Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required for Anthropic provider")
	}

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	return &Provider{
		client: client,
		bus:    b,
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "anthropic"
}

// Stream implements the Provider interface for Anthropic.
func (p *Provider) Stream(ctx context.Context, params llm.StreamParams) (*llm.StreamResult, error) {
	// Extract system messages (Anthropic requires these separately)
	systemMessages := ExtractSystemMessages(params.Messages)

	// Convert messages to Anthropic format
	antParams := anthropic.MessageNewParams{
		Model:     anthropic.Model(params.Model),
		MaxTokens: int64(params.MaxTokens),
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

	var usage llm.Usage
	var assistantContent string
	contentBlocks := []anthropic.ContentBlockUnion{}

	for stream.Next() {
		evt := stream.Current()

		switch evt.Type {
		case "message_delta":
			// Message delta contains usage information
			msgDelta := evt.AsMessageDelta()
			usage = llm.Usage{
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
			if blockDelta.Delta.Type == "text_delta" {
				if blockDelta.Delta.Text != "" {
					assistantContent += blockDelta.Delta.Text
					p.bus.Publish(bus.Event{
						Type:    bus.EventAgentContentDelta,
						Payload: bus.ContentDeltaPayload{Content: blockDelta.Delta.Text},
					})
				}
			} else if blockDelta.Delta.Type == "input_json_delta" {
				// Tool input is being streamed - accumulate to the last content block
				if len(contentBlocks) > 0 {
					idx := len(contentBlocks) - 1
					contentBlocks[idx].Input = append(contentBlocks[idx].Input, []byte(blockDelta.Delta.PartialJSON)...)
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		applog.Error("anthropic stream error", applog.M("error", err))
		return nil, err
	}

	// Build result message
	var resultMsg llm.Message

	if len(contentBlocks) > 0 {
		// Assistant requested tool calls
		toolCalls := []llm.ToolCall{}
		for _, block := range contentBlocks {
			if block.Type == "tool_use" {
				toolCalls = append(toolCalls, llm.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Arguments: string(block.Input),
				})
			}
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
