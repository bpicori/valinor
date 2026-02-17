package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	"github.com/openai/openai-go/v3"
	"valinor/internal/bus"
	"valinor/internal/tools"
)

//go:embed system_prompt.md
var systemPrompt string

// Agent manages conversation state and API calls. It subscribes to the bus for
// user intent events and publishes streaming events back.
type Agent struct {
	client   openai.Client
	model    string
	messages []openai.ChatCompletionMessageParamUnion
	tools    []tools.Tool
	sandbox  *tools.Sandbox
	bus      *bus.Bus
	sub      *bus.Subscription
}

// New creates an agent with the given client, model, workspace root path, and bus.
// The root path is created if it does not exist. Call Close when done.
func New(client openai.Client, model string, rootPath string, b *bus.Bus) (*Agent, error) {
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		return nil, err
	}
	sb, err := tools.NewSandbox(rootPath)
	if err != nil {
		return nil, err
	}
	return &Agent{
		client:   client,
		model:    model,
		messages: []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(systemPrompt)},
		tools:    tools.BuildTools(sb),
		sandbox:  sb,
		bus:      b,
		sub:      b.Subscribe(bus.EventUserMessage, bus.EventNewConversation),
	}, nil
}

// Run processes bus events until ctx is cancelled. Call in a goroutine.
func (a *Agent) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-a.sub.Ch():
			if !ok {
				return
			}
			switch ev.Type {
			case bus.EventUserMessage:
				p := ev.Payload.(bus.UserMessagePayload)
				_ = a.sendAndPublish(ctx, p.Content)
			case bus.EventNewConversation:
				a.NewConversation()
			}
		}
	}
}

// Close releases the sandbox root. Call when the agent is no longer needed.
func (a *Agent) Close() error {
	if a.sandbox != nil {
		err := a.sandbox.Close()
		a.sandbox = nil
		a.tools = nil
		return err
	}
	return nil
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// sendAndPublish sends a user message and publishes each streaming event to the bus.
func (a *Agent) sendAndPublish(ctx context.Context, userMessage string) error {
	a.messages = append(a.messages, openai.UserMessage(userMessage))

	params := openai.ChatCompletionNewParams{
		Model:           a.model,
		Messages:        a.messages,
		ReasoningEffort:  openai.ReasoningEffortMedium,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if len(a.tools) > 0 {
		defs := make([]openai.ChatCompletionToolUnionParam, len(a.tools))
		for i := range a.tools {
			defs[i] = a.tools[i].Def
		}
		params.Tools = defs
	}

	var usage Usage
	var assistantContent string

	for {
		stream := a.client.Chat.Completions.NewStreaming(ctx, params)
		acc := openai.ChatCompletionAccumulator{}

		for stream.Next() {
			chunk := stream.Current()
			acc.AddChunk(chunk)
			if chunk.Usage.TotalTokens > 0 {
				usage = Usage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
				}
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				assistantContent += delta.Content
				a.bus.Publish(bus.Event{
					Type:    bus.EventAgentContentDelta,
					Payload: bus.ContentDeltaPayload{Content: delta.Content},
				})
			}
			if delta.Refusal != "" {
				a.bus.Publish(bus.Event{
					Type:    bus.EventAgentRefusal,
					Payload: bus.RefusalPayload{Content: delta.Refusal},
				})
			}
		}

		if err := stream.Err(); err != nil {
			a.bus.Publish(bus.Event{
				Type:    bus.EventAgentError,
				Payload: bus.AgentErrorPayload{Error: err},
			})
			return err
		}

		if len(acc.Choices) == 0 {
			a.bus.Publish(bus.Event{
				Type: bus.EventAgentDone,
				Payload: bus.AgentDonePayload{
					PromptTokens:     usage.PromptTokens,
					CompletionTokens: usage.CompletionTokens,
					TotalTokens:      usage.TotalTokens,
				},
			})
			return nil
		}
		msg := acc.Choices[0].Message

		if len(msg.ToolCalls) == 0 {
			a.messages = append(a.messages, openai.AssistantMessage(assistantContent))
			a.bus.Publish(bus.Event{
				Type: bus.EventAgentDone,
				Payload: bus.AgentDonePayload{
					PromptTokens:     usage.PromptTokens,
					CompletionTokens: usage.CompletionTokens,
					TotalTokens:      usage.TotalTokens,
				},
			})
			return nil
		}

		a.messages = append(a.messages, msg.ToParam())

		for _, tc := range msg.ToolCalls {
			var args json.RawMessage
			if tc.Function.Arguments != "" {
				args = json.RawMessage(tc.Function.Arguments)
			}
			a.bus.Publish(bus.Event{
				Type: bus.EventAgentToolCallStart,
				Payload: bus.ToolCallStartPayload{
					ToolCallID: tc.ID,
					ToolName:   tc.Function.Name,
					ToolArgs:   string(args),
				},
			})
			var result string
			for _, t := range a.tools {
				if t.Name == tc.Function.Name {
					var execErr error
					result, execErr = t.Exec(args)
					if execErr != nil {
						result = fmt.Sprintf("error: %v", execErr)
					}
					break
				}
			}
			a.bus.Publish(bus.Event{
				Type: bus.EventAgentToolCallDone,
				Payload: bus.ToolCallDonePayload{
					ToolCallID:  tc.ID,
					ToolName:    tc.Function.Name,
					ToolResult:  result,
				},
			})
			a.messages = append(a.messages, openai.ToolMessage(result, tc.ID))
		}

		params.Messages = a.messages
		assistantContent = ""
	}
}

// NewConversation clears the conversation history, keeping only the system prompt.
func (a *Agent) NewConversation() {
	a.messages = []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(systemPrompt)}
}
