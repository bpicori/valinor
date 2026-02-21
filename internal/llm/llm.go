package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"valinor/internal/applog"
	"valinor/internal/bus"
	agentcontext "valinor/internal/context"
	"valinor/internal/sandbox"
	"valinor/internal/tools"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared/constant"
)

// LLM runs model turns using the provided context runtime.
type LLM struct {
	client        openai.Client
	model         string
	bus           *bus.Bus
	maxIterations int
	tools         []tools.Tool
}

// RunRequest configures a single run invocation.
type RunRequest struct {
	Context agentcontext.RunContext
}

// RunResult returns the updated messages after the loop.
type RunResult struct {
	Messages []agentcontext.Message
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// New creates an LLM runtime and initializes the OpenAI client.
func New(apiKey, baseURL, model string, rootPath string, b *bus.Bus, maxIterations int) (*LLM, error) {
	if maxIterations <= 0 {
		maxIterations = 20
	}
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)
	rt, err := sandbox.NewRuntime(rootPath)
	if err != nil {
		return nil, err
	}
	return &LLM{
		client:        client,
		model:         model,
		bus:           b,
		maxIterations: maxIterations,
		tools:         tools.BuildTools(rt),
	}, nil
}

// Run executes one LLM operation: reset, restore, or user-message turn.
func (l *LLM) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	messages := append([]agentcontext.Message{}, req.Context.Messages...)
	if len(messages) == 0 {
		return RunResult{}, nil
	}
	params := l.buildParams(messages)

	var usage Usage
	var assistantContent string
	iterations := 0

	for {
		if iterations >= l.maxIterations {
			l.bus.Publish(bus.Event{
				Type: bus.EventAgentError,
				Payload: bus.AgentErrorPayload{
					Error: fmt.Errorf("max agent iterations (%d) reached; stopping to prevent runaway loop", l.maxIterations),
				},
			})
			return RunResult{}, fmt.Errorf("max agent iterations (%d) reached", l.maxIterations)
		}
		iterations++

		applog.Debug("llm iteration start", applog.M("iteration", iterations))

		stream := l.client.Chat.Completions.NewStreaming(ctx, params)
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
				l.bus.Publish(bus.Event{
					Type: bus.EventAgentStepTokens,
					Payload: bus.StepTokenUsagePayload{
						Step:             iterations,
						PromptTokens:     usage.PromptTokens,
						CompletionTokens: usage.CompletionTokens,
						TotalTokens:      usage.TotalTokens,
					},
				})
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				assistantContent += delta.Content
				l.bus.Publish(bus.Event{
					Type:    bus.EventAgentContentDelta,
					Payload: bus.ContentDeltaPayload{Content: delta.Content},
				})
			}
			if delta.Refusal != "" {
				l.bus.Publish(bus.Event{
					Type:    bus.EventAgentRefusal,
					Payload: bus.RefusalPayload{Content: delta.Refusal},
				})
			}
		}

		if err := stream.Err(); err != nil {
			applog.Error("llm stream error", applog.M("error", err))
			l.bus.Publish(bus.Event{
				Type:    bus.EventAgentError,
				Payload: bus.AgentErrorPayload{Error: err},
			})
			return RunResult{}, err
		}
		if assistantContent != "" {
			l.bus.Publish(bus.Event{
				Type:    bus.EventAgentContentFull,
				Payload: bus.ContentFullPayload{Content: assistantContent},
			})
		}

		if len(acc.Choices) == 0 {
			applog.Debug("llm iteration done", applog.M("iteration", iterations))
			l.bus.Publish(bus.Event{
				Type: bus.EventAgentDone,
				Payload: bus.AgentDonePayload{
					PromptTokens:     usage.PromptTokens,
					CompletionTokens: usage.CompletionTokens,
					TotalTokens:      usage.TotalTokens,
				},
			})
			return RunResult{Messages: messages}, nil
		}
		msg := acc.Choices[0].Message

		if len(msg.ToolCalls) == 0 {
			messages = append(messages, agentcontext.Message{
				Role:    agentcontext.RoleAssistant,
				Content: assistantContent,
			})
			l.bus.Publish(bus.Event{
				Type: bus.EventAgentDone,
				Payload: bus.AgentDonePayload{
					PromptTokens:     usage.PromptTokens,
					CompletionTokens: usage.CompletionTokens,
					TotalTokens:      usage.TotalTokens,
				},
			})
			applog.Debug("llm iteration done", applog.M("iteration", iterations))
			return RunResult{Messages: messages}, nil
		}

		toolCalls := make([]agentcontext.ToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, agentcontext.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		messages = append(messages, agentcontext.Message{
			Role:      agentcontext.RoleAssistantToolCalls,
			ToolCalls: toolCalls,
		})

		for _, tc := range msg.ToolCalls {
			var args json.RawMessage
			if tc.Function.Arguments != "" {
				args = json.RawMessage(tc.Function.Arguments)
			}
			applog.Debug("llm tool call start", applog.M("step", iterations, "tool_call_id", tc.ID, "tool", tc.Function.Name, "args", string(args)))
			l.bus.Publish(bus.Event{
				Type: bus.EventAgentThinking,
				Payload: bus.ThinkingPayload{
					Step:    iterations,
					Content: "executing tool: " + tc.Function.Name,
				},
			})
			l.bus.Publish(bus.Event{
				Type: bus.EventAgentToolCallStart,
				Payload: bus.ToolCallStartPayload{
					ToolCallID: tc.ID,
					ToolName:   tc.Function.Name,
					ToolArgs:   string(args),
				},
			})
			result, ok := l.executeTool(tc.Function.Name, args)
			if !ok {
				result = "error: tool not found"
			}
			applog.Debug("llm tool call done", applog.M("step", iterations, "tool_call_id", tc.ID, "tool", tc.Function.Name, "found", ok, "result", result))
			l.bus.Publish(bus.Event{
				Type: bus.EventAgentToolCallDone,
				Payload: bus.ToolCallDonePayload{
					ToolCallID: tc.ID,
					ToolName:   tc.Function.Name,
					ToolResult: result,
				},
			})
			messages = append(messages, agentcontext.Message{
				Role:       agentcontext.RoleTool,
				ToolCallID: tc.ID,
				Content:    result,
			})
		}

		params.Messages = toOpenAIMessages(messages)
		assistantContent = ""
	}
}

func (l *LLM) buildParams(messages []agentcontext.Message) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:           l.model,
		Messages:        toOpenAIMessages(messages),
		ReasoningEffort: openai.ReasoningEffortMedium,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if len(l.tools) > 0 {
		defs := make([]openai.ChatCompletionToolUnionParam, len(l.tools))
		for i := range l.tools {
			defs[i] = l.tools[i].Def
		}
		params.Tools = defs
	}
	return params
}

func toOpenAIMessages(messages []agentcontext.Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case agentcontext.RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case agentcontext.RoleUser:
			out = append(out, openai.UserMessage(m.Content))
		case agentcontext.RoleAssistant:
			out = append(out, openai.AssistantMessage(m.Content))
		case agentcontext.RoleAssistantToolCalls:
			toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID:   tc.ID,
						Type: constant.Function("function"),
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					},
				})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role:      constant.Assistant("assistant"),
					ToolCalls: toolCalls,
				},
			})
		case agentcontext.RoleTool:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return out
}

func (l *LLM) executeTool(toolName string, args json.RawMessage) (string, bool) {
	for _, t := range l.tools {
		if t.Name != toolName {
			continue
		}
		result, err := t.Exec(args)
		if err != nil {
			return fmt.Sprintf("error: %v", err), true
		}
		return result, true
	}
	return "", false
}
