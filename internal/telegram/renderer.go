// Package telegram provides event-to-message rendering for Telegram.
package telegram

import (
	"fmt"
	"strings"

	"valinor/internal/event"
	"valinor/internal/gateway"
)

// MaxTelegramMessageLength is Telegram's maximum message length.
const MaxTelegramMessageLength = 4096

// Pre-allocated replacer for MarkdownV2 escaping to avoid repeated allocations.
// These are the characters that MUST be escaped in Telegram MarkdownV2.
var mdReplacer = strings.NewReplacer(
	"_", "\\_",
	"[", "\\[",
	"]", "\\]",
	"(", "\\(",
	")", "\\)",
	"~", "\\~",
	"`", "\\`",
	">", "\\>",
	"#", "\\#",
	"+", "\\+",
	"=", "\\=",
	"|", "\\|",
	"{", "\\{",
	"}", "\\}",
	".", "\\.",
	"!", "\\!",
)

// RenderResult contains the rendered Telegram message.
type RenderResult struct {
	Text        string
	ParseMode   string
	ReplyMarkup *InlineKeyboardMarkup
	// IsStreaming indicates this is a streaming delta that should edit
	// the existing streaming message if one exists
	IsStreaming bool
	// IsFinal indicates this is a terminal event (done/error/cancelled)
	IsFinal bool
}

// Renderer converts gateway events to Telegram message formats.
// This is stateless - all mutable state lives in ChatState.
type Renderer struct{}

// NewRenderer creates a new stateless event renderer.
func NewRenderer() *Renderer {
	return &Renderer{}
}

// RenderEvent converts a gateway event to a Telegram message.
// Returns nil if the event should not result in a message.
// This is a pure function - all state decisions are made by the caller.
func (r *Renderer) RenderEvent(ev gateway.Event) *RenderResult {
	switch ev.Type {
	case event.AgentContentDelta:
		return r.renderContentDelta(ev.Payload)
	case event.AgentContentFull:
		return r.renderContentFull(ev.Payload)
	case event.AgentThinking:
		return r.renderThinking(ev.Payload)
	case event.AgentDone:
		return r.renderDone(ev.Payload)
	case event.AgentError:
		return r.renderError(ev.Payload)
	case event.AgentCancelled:
		return r.renderCancelled()
	case event.AgentToolCallStart:
		// Tool lifecycle events are noisy for Telegram users; keep them internal only.
		return nil
	case event.AgentToolCallDone:
		return nil
	case event.SessionAttached:
		return r.renderSessionAttached(ev.Payload)
	case event.ChangeModelEvent:
		return r.renderModelChanged(ev.Payload)
	default:
		// Events like AgentStepTokens, ContextTokens don't need messages
		return nil
	}
}

// renderContentDelta renders a streaming content delta.
// Returns nil to skip message updates - content is accumulated but only final result is sent.
func (r *Renderer) renderContentDelta(payload interface{}) *RenderResult {
	// Delta chunks accumulate content in ChatState but don't trigger message updates.
	// Only AgentDone → RenderFinalContent sends the final message.
	return nil
}

// renderContentFull renders a full content replacement.
// Returns nil to skip message updates - content is accumulated but only final result is sent.
func (r *Renderer) renderContentFull(payload interface{}) *RenderResult {
	// Full content replacements update ChatState but don't trigger message updates.
	// Only AgentDone → RenderFinalContent sends the final message.
	return nil
}

// renderThinking renders a thinking message.
func (r *Renderer) renderThinking(payload interface{}) *RenderResult {
	p, ok := payload.(gateway.ThinkingPayload)
	if !ok {
		return nil
	}

	return &RenderResult{
		Text:      "🤔 *Thinking\\.\\.\\.*\n_" + EscapeMarkdown(p.Content) + "_",
		ParseMode: "MarkdownV2",
	}
}

// renderDone renders the final done message.
// Note: This sends as a new message rather than editing, to avoid replacing
// the streaming content that was already delivered.
func (r *Renderer) renderDone(payload interface{}) *RenderResult {
	p, ok := payload.(gateway.AgentDonePayload)
	if !ok {
		// If no payload, don't show anything - the streaming content is already visible
		return nil
	}

	var sb strings.Builder
	sb.WriteString("✅ *Complete*")

	if p.TotalTokens > 0 {
		sb.WriteString("\n\n📊 *Token Usage:*\n")
		sb.WriteString(EscapeMarkdownf("• Total: %d\n", p.TotalTokens))
		sb.WriteString(EscapeMarkdownf("• Prompt: %d\n", p.PromptTokens))
		sb.WriteString(EscapeMarkdownf("• Completion: %d", p.CompletionTokens))
	}

	// Send as a new message (not streaming) to preserve the streaming content
	return &RenderResult{
		Text:      sb.String(),
		ParseMode: "MarkdownV2",
		IsFinal:   true,
	}
}

// renderError renders an error message.
// This edits the streaming message to show the error.
func (r *Renderer) renderError(payload interface{}) *RenderResult {
	p, ok := payload.(gateway.AgentErrorPayload)
	if !ok {
		return &RenderResult{
			Text:      "❌ *Error occurred*",
			ParseMode: "MarkdownV2",
			IsFinal:   true,
		}
	}

	var errMsg string
	if p.Error != nil {
		errMsg = p.Error.Error()
	}
	text := "❌ *Error:*\n" + EscapeMarkdown(errMsg)

	return &RenderResult{
		Text:      text,
		ParseMode: "MarkdownV2",
		IsFinal:   true,
	}
}

// renderCancelled renders a cancellation message.
// This edits the streaming message to show it was cancelled.
func (r *Renderer) renderCancelled() *RenderResult {
	return &RenderResult{
		Text:      "🛑 _Cancelled_",
		ParseMode: "MarkdownV2",
		IsFinal:   true,
	}
}

// renderSessionAttached renders a session attachment notification.
func (r *Renderer) renderSessionAttached(payload interface{}) *RenderResult {
	p, ok := payload.(gateway.SessionAttachedPayload)
	if !ok {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("📎 *Session attached*\n")
	sb.WriteString(EscapeMarkdownf("• Session: %s\n", p.SessionKey))
	sb.WriteString(EscapeMarkdownf("• Provider: %s\n", p.Provider))
	sb.WriteString(EscapeMarkdownf("• Model: %s", p.Model))

	return &RenderResult{
		Text:      sb.String(),
		ParseMode: "MarkdownV2",
	}
}

// renderModelChanged renders a model change notification.
func (r *Renderer) renderModelChanged(payload interface{}) *RenderResult {
	p, ok := payload.(gateway.ChangeProviderModelPayload)
	if !ok {
		return nil
	}

	text := EscapeMarkdownf("🔄 *Switched to* %s / %s", p.Provider, p.Model)

	return &RenderResult{
		Text:      text,
		ParseMode: "MarkdownV2",
	}
}

// RenderFinalContent renders the final assistant content as a single Telegram message.
// Converts standard markdown to Telegram MarkdownV2 format.
func (r *Renderer) RenderFinalContent(content string) *RenderResult {
	if content == "" {
		return nil
	}

	text := content
	if len(text) > MaxTelegramMessageLength {
		text = text[:MaxTelegramMessageLength-3] + "..."
	}

	return &RenderResult{
		Text:      ConvertMarkdownToTelegram(text),
		ParseMode: "MarkdownV2",
		IsFinal:   true,
	}
}

// EscapeMarkdown escapes text for Telegram's MarkdownV2 format.
// This escapes all special characters and should be used for plain text
// that shouldn't have any markdown formatting interpreted.
func EscapeMarkdown(text string) string {
	return mdReplacer.Replace(text)
}

// EscapeMarkdownf escapes formatted text for MarkdownV2.
func EscapeMarkdownf(format string, args ...interface{}) string {
	text := EscapeMarkdown(format)
	// Re-escape any args that might be strings
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			args[i] = EscapeMarkdown(s)
		}
	}
	return fmt.Sprintf(text, args...)
}

// RenderStatus renders the current session status for the /status command.
func (r *Renderer) RenderStatus(sessionKey, provider, model string) *RenderResult {
	var sb strings.Builder
	sb.WriteString("ℹ️ *Current Status*\n")

	if sessionKey != "" {
		sb.WriteString(EscapeMarkdownf("• Session: %s\n", sessionKey))
	} else {
		sb.WriteString("• Session: _not attached_\n")
	}

	if provider != "" {
		sb.WriteString(EscapeMarkdownf("• Provider: %s\n", provider))
	}

	if model != "" {
		sb.WriteString(EscapeMarkdownf("• Model: %s", model))
	}

	return &RenderResult{
		Text:      sb.String(),
		ParseMode: "MarkdownV2",
	}
}
