package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	"github.com/openai/openai-go/v3"
	"valinor/internal/tools"
)

//go:embed system_prompt.md
var systemPrompt string

// Agent manages conversation state and API calls.
type Agent struct {
	client   openai.Client
	model    string
	messages []openai.ChatCompletionMessageParamUnion
	tools    []tools.Tool
	sandbox  *tools.Sandbox
}

// New creates an agent with the given client, model, and workspace root path.
// The root path is created if it does not exist. Call Close when done.
func New(client openai.Client, model string, rootPath string) (*Agent, error) {
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
	}, nil
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

// EventType identifies what kind of streaming event occurred.
type EventType int

const (
	EventContentDelta  EventType = iota // Streamed content chunk
	EventRefusal                        // Model refusal
	EventToolCallStart                  // Tool call initiated
	EventToolCallDone                   // Tool call finished with result
	EventDone                           // Agent finished (includes usage)
	EventError                          // An error occurred
)

// Event represents a streaming event from the agent.
type Event struct {
	Type       EventType
	Content    string // For ContentDelta, Refusal
	ToolCallID string // For ToolCallStart, ToolCallDone
	ToolName   string // For ToolCallStart, ToolCallDone
	ToolArgs   string // For ToolCallStart (JSON string)
	ToolResult string // For ToolCallDone
	Usage      Usage  // For EventDone
	Error      error  // For EventError
}

// Send sends a user message and calls onEvent for each streaming event.
// It blocks until the full response (including all tool calls) is complete.
func (a *Agent) Send(ctx context.Context, userMessage string, onEvent func(Event)) error {
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
				onEvent(Event{Type: EventContentDelta, Content: delta.Content})
			}
			if delta.Refusal != "" {
				onEvent(Event{Type: EventRefusal, Content: delta.Refusal})
			}
		}

		if err := stream.Err(); err != nil {
			onEvent(Event{Type: EventError, Error: err})
			return err
		}

		if len(acc.Choices) == 0 {
			onEvent(Event{Type: EventDone, Usage: usage})
			return nil
		}
		msg := acc.Choices[0].Message

		if len(msg.ToolCalls) == 0 {
			a.messages = append(a.messages, openai.AssistantMessage(assistantContent))
			onEvent(Event{Type: EventDone, Usage: usage})
			return nil
		}

		a.messages = append(a.messages, msg.ToParam())

		for _, tc := range msg.ToolCalls {
			var args json.RawMessage
			if tc.Function.Arguments != "" {
				args = json.RawMessage(tc.Function.Arguments)
			}
			onEvent(Event{
				Type:       EventToolCallStart,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				ToolArgs:   string(args),
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
			onEvent(Event{
				Type:       EventToolCallDone,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				ToolResult: result,
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
