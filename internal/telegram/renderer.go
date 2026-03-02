// Package telegram provides event-to-message rendering for Telegram.
package telegram

import (
	"strings"

	"valinor/internal/event"
	"valinor/internal/gateway"
)

// MaxTelegramMessageLength is Telegram's maximum message length.
const MaxTelegramMessageLength = 4096

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
	case event.AgentContentDelta, event.AgentContentFull:
		// Content delta/full events accumulate content in ChatState but don't trigger
		// message updates. Only AgentDone → RenderFinalContent sends the final message.
		return nil
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

// RenderStatus renders the current session status for the /status command.
func (r *Renderer) RenderStatus(sessionKey, provider, model string) *RenderResult {
	var text string
	text = "ℹ️ *Current Status*\n"

	if sessionKey != "" {
		text += EscapeMarkdownf("• Session: %s\n", sessionKey)
	} else {
		text += "• Session: _not attached_\n"
	}

	if provider != "" {
		text += EscapeMarkdownf("• Provider: %s\n", provider)
	}

	if model != "" {
		text += EscapeMarkdownf("• Model: %s", model)
	}

	return &RenderResult{
		Text:      text,
		ParseMode: "MarkdownV2",
	}
}
