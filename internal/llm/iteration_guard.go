package llm

import (
	"context"
	"errors"
	"fmt"

	agentcontext "valinor/internal/context"
	"valinor/internal/event"
	"valinor/internal/logger"
)

const iterationWarningLead = 3

func iterationBudgetWarning(current, max int) string {
	remaining := max - current
	if remaining <= 0 || remaining > iterationWarningLead {
		return ""
	}

	switch remaining {
	case 3:
		return fmt.Sprintf(
			"[System] Iteration budget warning: you are on iteration %d of %d (%d remaining). Prefer replying with evidence collected so far rather than more tool calls.",
			current, max, remaining,
		)
	case 2:
		return fmt.Sprintf(
			"[System] Iteration budget warning: you are on iteration %d of %d (%d remaining). Wrap up soon — answer the user with partial findings if needed.",
			current, max, remaining,
		)
	case 1:
		return fmt.Sprintf(
			"[System] Iteration budget warning: you are on iteration %d of %d (1 remaining). The next turn will be tool-less — reply now unless one final tool call is essential.",
			current, max,
		)
	default:
		return ""
	}
}

func (l *LLM) runGracefulFinalTurn(
	ctx context.Context,
	req RunRequest,
	messages []agentcontext.Message,
	usage Usage,
	toolsUsed map[string]struct{},
	publishEvent func(event.EventType, any),
) (RunResult, error) {
	logger.Info("llm max iterations reached; running graceful final turn", logger.BuildMetadata("max_iterations", l.maxIterations))

	messages = append(messages, agentcontext.Message{
		Role: agentcontext.RoleUser,
		Content: fmt.Sprintf(
			"[System] Maximum iteration limit reached (%d). Do not call any tools. Reply now with your best answer using evidence already collected. State what blocked you if relevant. Tell the user they can send another message if they want you to continue researching.",
			l.maxIterations,
		),
	})

	if req.Compressor != nil {
		var compressionResult CompressionResult
		messages, compressionResult = req.Compressor.MaybeCompress(ctx, messages)
		publishCompressionResult(publishEvent, compressionResult)
	}

	result, err := l.provider.Stream(ctx, StreamParams{
		Messages: l.toProviderMessages(messages),
		Tools:    nil,
		Model:    l.model,
		OnDelta: func(delta StreamDelta) {
			if delta.Content != "" {
				publishEvent(event.AgentContentDelta, event.ContentDeltaPayload{Content: delta.Content})
			}
			if delta.Refusal != "" {
				publishEvent(event.AgentRefusal, event.RefusalPayload{Content: delta.Refusal})
			}
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Info("llm graceful final turn cancelled by user", nil)
			publishEvent(event.AgentCancelled, event.AgentCancelledPayload{})
			return RunResult{Messages: messages, Usage: usage}, nil
		}
		logger.Error("llm graceful final turn stream error", logger.BuildMetadata("error", err))
		publishEvent(event.AgentError, event.AgentErrorPayload{Error: err})
		return RunResult{}, err
	}

	usage.PromptTokens += result.Usage.PromptTokens
	usage.CompletionTokens += result.Usage.CompletionTokens
	usage.TotalTokens += result.Usage.TotalTokens

	content := result.Message.Content
	if content == "" && len(result.Message.ToolCalls) > 0 {
		content = fmt.Sprintf(
			"I reached the maximum iteration limit (%d) while researching your request. I could not finish gathering all data. Send another message if you want me to continue.",
			l.maxIterations,
		)
	}

	messages = append(messages, agentcontext.Message{
		Role:    agentcontext.RoleAssistant,
		Content: content,
	})

	if content != "" {
		publishEvent(event.AgentContentFull, event.ContentFullPayload{Content: content})
	}
	publishEvent(event.AgentDone, event.AgentDonePayload{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
	})

	if shouldRunMemoryPass(toolsUsed) {
		memoryUsage := l.runSilentMemoryPass(ctx, req, messages)
		usage.PromptTokens += memoryUsage.PromptTokens
		usage.CompletionTokens += memoryUsage.CompletionTokens
		usage.TotalTokens += memoryUsage.TotalTokens
	}

	return RunResult{Messages: messages, Usage: usage}, nil
}