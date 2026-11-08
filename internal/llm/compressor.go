package llm

import (
	"context"
	"fmt"
	"strings"

	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/logger"
)

const (
	compressionThreshold  = 0.85
	compressionPrompt = `Summarize the following conversation excerpt. Preserve:
- Key decisions and their rationale
- File paths and code changes mentioned
- User preferences and corrections
- Unresolved tasks or open questions
- Tool results that contain important data

Omit: greetings, repeated tool errors, verbose command output.
Write in third person, past tense. Be concise but complete.
Max 2000 tokens.`
)

// CompressionResult reports the outcome of a MaybeCompress call.
type CompressionResult struct {
	Applied            bool
	MessagesCompressed int
	TokensBefore       int64
	TokensAfter        int64
	Limit              int64
	Err                error
}

// Compressor summarizes older conversation turns to stay within model context limits.
type Compressor struct {
	provider     Provider
	model        string
	windowTokens int
}

// NewCompressor creates a compressor for a model context window.
func NewCompressor(provider Provider, model string, windowTokens int) *Compressor {
	if windowTokens <= 0 {
		windowTokens = config.Must().Context.WindowTokens
	}
	return &Compressor{provider: provider, model: model, windowTokens: windowTokens}
}

// Limit returns the token threshold at which compression triggers.
func (c *Compressor) Limit() int64 {
	return int64(float64(c.windowTokens) * compressionThreshold)
}

// MaybeCompress replaces the middle of the conversation with a summary when over threshold.
// Fail-open: returns the original messages on error or when there is nothing to compress.
// priorTokens is the provider-reported prompt size of the previous turn, which is
// more accurate than the local estimate for long sessions.
func (c *Compressor) MaybeCompress(ctx context.Context, messages []agentcontext.Message, priorTokens int64) ([]agentcontext.Message, CompressionResult) {
	return c.compress(ctx, messages, priorTokens, c.Limit(), false)
}

// ForceCompress summarizes regardless of the current size. It is used after the
// provider rejects a request for exceeding the context window.
func (c *Compressor) ForceCompress(ctx context.Context, messages []agentcontext.Message, priorTokens int64) ([]agentcontext.Message, CompressionResult) {
	return c.compress(ctx, messages, priorTokens, c.Limit(), true)
}

func (c *Compressor) compress(ctx context.Context, messages []agentcontext.Message, priorTokens, limit int64, force bool) ([]agentcontext.Message, CompressionResult) {
	tokensBefore := EstimatePromptTokens(messages)
	if priorTokens > tokensBefore {
		tokensBefore = priorTokens
	}
	if !force && tokensBefore <= limit {
		return messages, CompressionResult{TokensBefore: tokensBefore, TokensAfter: tokensBefore, Limit: limit}
	}

	const headEnd = 1 // keep system prompt
	tailStart := len(messages) - config.Must().Context.PreserveRecentMessages
	if tailStart < headEnd {
		return messages, CompressionResult{TokensBefore: tokensBefore, TokensAfter: tokensBefore, Limit: limit}
	}
	for tailStart > headEnd && messages[tailStart].Role == agentcontext.RoleTool {
		tailStart--
	}
	if tailStart <= headEnd {
		return messages, CompressionResult{TokensBefore: tokensBefore, TokensAfter: tokensBefore, Limit: limit}
	}

	middle := messages[headEnd:tailStart]
	summary, err := c.summarize(ctx, middle)
	if err != nil {
		logger.Warning("context compression failed", logger.BuildMetadata("error", err))
		return messages, CompressionResult{
			TokensBefore: tokensBefore,
			TokensAfter:  tokensBefore,
			Limit:        limit,
			Err:          err,
		}
	}

	out := make([]agentcontext.Message, 0, headEnd+1+len(messages)-tailStart)
	out = append(out, messages[:headEnd]...)
	out = append(out, agentcontext.Message{
		Role:    agentcontext.RoleUser,
		Content: fmt.Sprintf("[Context Summary — %d messages compressed]\n%s", len(middle), summary),
	})
	out = append(out, messages[tailStart:]...)
	tokensAfter := EstimatePromptTokens(out)
	return out, CompressionResult{
		Applied:            true,
		MessagesCompressed: len(middle),
		TokensBefore:       tokensBefore,
		TokensAfter:        tokensAfter,
		Limit:              limit,
	}
}

func (c *Compressor) summarize(ctx context.Context, messages []agentcontext.Message) (string, error) {
	var sb strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case agentcontext.RoleUser:
			fmt.Fprintf(&sb, "User: %s\n\n", msg.Content)
		case agentcontext.RoleAssistant:
			fmt.Fprintf(&sb, "Assistant: %s\n\n", msg.Content)
		case agentcontext.RoleAssistantToolCalls:
			sb.WriteString("Assistant (tool calls):\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&sb, "- %s(%s)\n", tc.Name, tc.Arguments)
			}
			sb.WriteString("\n")
		case agentcontext.RoleTool:
			fmt.Fprintf(&sb, "Tool result [%s]: %s\n\n", msg.ToolCallID, msg.Content)
		case agentcontext.RoleSystem:
			fmt.Fprintf(&sb, "System: %s\n\n", msg.Content)
		}
	}

	var content strings.Builder
	result, err := c.provider.Stream(ctx, StreamParams{
		Messages: []Message{
			{Role: RoleSystem, Content: compressionPrompt},
			{Role: RoleUser, Content: strings.TrimSpace(sb.String())},
		},
		Model: c.model,
		OnDelta: func(delta StreamDelta) {
			content.WriteString(delta.Content)
		},
	})
	if err != nil {
		return "", err
	}
	if summary := strings.TrimSpace(content.String()); summary != "" {
		return summary, nil
	}
	return strings.TrimSpace(result.Message.Content), nil
}