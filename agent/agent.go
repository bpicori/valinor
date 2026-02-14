package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/openai/openai-go/v3"
	"valinor/tools"
)

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
		messages: nil,
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

// Send sends a user message, streams the assistant's reply and logs to the provided writers, and returns the reply content and usage statistics.
// contentOut: main reply, refusalOut: refusals, toolOut: tool logs (nil to suppress).
func (a *Agent) Send(ctx context.Context, userMessage string, contentOut, refusalOut, toolOut io.Writer) (assistantContent string, usage Usage, err error) {
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
				if contentOut != nil {
					_, _ = contentOut.Write([]byte(delta.Content))
				}
			}
			if delta.Refusal != "" && refusalOut != nil {
				_, _ = refusalOut.Write([]byte(delta.Refusal))
			}
		}

		if err = stream.Err(); err != nil {
			return "", usage, err
		}

		if len(acc.Choices) == 0 {
			return assistantContent, usage, nil
		}
		msg := acc.Choices[0].Message

		if len(msg.ToolCalls) == 0 {
			a.messages = append(a.messages, openai.AssistantMessage(assistantContent))
			return assistantContent, usage, nil
		}

		a.messages = append(a.messages, msg.ToParam())

		for _, tc := range msg.ToolCalls {
			var args json.RawMessage
			if tc.Function.Arguments != "" {
				args = json.RawMessage(tc.Function.Arguments)
			}
			if toolOut != nil {
				fmt.Fprintf(toolOut, "[tool] %s %s\n", tc.Function.Name, string(args))
			}
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
			a.messages = append(a.messages, openai.ToolMessage(result, tc.ID))
		}

		params.Messages = a.messages
		assistantContent = ""
	}
}

// NewConversation clears the conversation history.
func (a *Agent) NewConversation() {
	a.messages = nil
}
