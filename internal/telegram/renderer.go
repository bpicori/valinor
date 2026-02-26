// Package telegram provides event-to-message rendering for Telegram.
package telegram

import (
	"fmt"
	"strings"

	"valinor/internal/event"
	"valinor/internal/router"
)

// MaxTelegramMessageLength is Telegram's maximum message length.
const MaxTelegramMessageLength = 4096

// RenderResult contains the rendered Telegram message.
type RenderResult struct {
	Text            string
	ParseMode       string
	ReplyMarkup     *InlineKeyboardMarkup
	EditMessageID   int64 // Set to non-zero to edit instead of sending new message
}

// Renderer converts gateway events to Telegram message formats.
type Renderer struct {
	// EnableMarkdown enables Markdown parsing in outgoing messages
	EnableMarkdown bool
	// streamingMessageID tracks the current streaming message for delta updates
	streamingMessageID int64
}

// NewRenderer creates a new event renderer.
func NewRenderer() *Renderer {
	return &Renderer{
		EnableMarkdown: true,
	}
}

// RenderEvent converts a router event to a Telegram message.
// Returns nil if the event should not result in a message.
func (r *Renderer) RenderEvent(ev router.Event) *RenderResult {
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
		return r.renderCancelled(ev.Payload)
	case event.AgentToolCallStart:
		return r.renderToolCallStart(ev.Payload)
	case event.AgentToolCallDone:
		return r.renderToolCallDone(ev.Payload)
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
func (r *Renderer) renderContentDelta(payload interface{}) *RenderResult {
	p, ok := payload.(router.ContentDeltaPayload)
	if !ok {
		return nil
	}

	// Truncate if too long
	text := p.Content
	if len(text) > MaxTelegramMessageLength {
		text = text[:MaxTelegramMessageLength-3] + "..."
	}

	return &RenderResult{
		Text:          text,
		ParseMode:     "",
		EditMessageID: r.streamingMessageID,
	}
}

// renderContentFull renders a full content replacement.
func (r *Renderer) renderContentFull(payload interface{}) *RenderResult {
	p, ok := payload.(router.ContentFullPayload)
	if !ok {
		return nil
	}

	text := p.Content
	if len(text) > MaxTelegramMessageLength {
		text = text[:MaxTelegramMessageLength-3] + "..."
	}

	// Escape markdown characters to avoid formatting errors
	text = EscapeMarkdown(text)

	return &RenderResult{
		Text:          text,
		ParseMode:     "MarkdownV2",
		EditMessageID: r.streamingMessageID,
	}
}

// renderThinking renders a thinking message.
func (r *Renderer) renderThinking(payload interface{}) *RenderResult {
	p, ok := payload.(router.ThinkingPayload)
	if !ok {
		return nil
	}

	return &RenderResult{
		Text:      "🤔 *Thinking...*\n_" + EscapeMarkdown(p.Content) + "_",
		ParseMode: "MarkdownV2",
	}
}

// renderDone renders the final done message.
// Note: This sends as a new message rather than editing, to avoid replacing
// the streaming content that was already delivered.
func (r *Renderer) renderDone(payload interface{}) *RenderResult {
	p, ok := payload.(router.AgentDonePayload)
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

	// Send as a new message (no EditMessageID) to preserve the streaming content
	return &RenderResult{
		Text:          sb.String(),
		ParseMode:     "MarkdownV2",
		EditMessageID: 0, // New message, don't edit
	}
}

// renderError renders an error message.
func (r *Renderer) renderError(payload interface{}) *RenderResult {
	p, ok := payload.(router.AgentErrorPayload)
	if !ok {
		return &RenderResult{
			Text:      "❌ *Error occurred*",
			ParseMode: "MarkdownV2",
		}
	}

	var errMsg string
	if p.Error != nil {
		errMsg = p.Error.Error()
	}
	text := "❌ *Error:*\n" + EscapeMarkdown(errMsg)

	return &RenderResult{
		Text:          text,
		ParseMode:     "MarkdownV2",
		EditMessageID: r.streamingMessageID,
	}
}

// renderCancelled renders a cancellation message.
func (r *Renderer) renderCancelled(payload interface{}) *RenderResult {
	return &RenderResult{
		Text:          "🛑 _Cancelled_",
		ParseMode:     "MarkdownV2",
		EditMessageID: r.streamingMessageID,
	}
}

// renderToolCallStart renders a tool call start indicator.
func (r *Renderer) renderToolCallStart(payload interface{}) *RenderResult {
	p, ok := payload.(router.ToolCallStartPayload)
	if !ok {
		return nil
	}

	text := EscapeMarkdownf("🔧 *Using tool:* `%s`", p.ToolName)

	return &RenderResult{
		Text:      text,
		ParseMode: "MarkdownV2",
	}
}

// renderToolCallDone renders a tool call completion indicator.
func (r *Renderer) renderToolCallDone(payload interface{}) *RenderResult {
	p, ok := payload.(router.ToolCallDonePayload)
	if !ok {
		return nil
	}

	text := EscapeMarkdownf("✅ *Tool finished:* `%s`", p.ToolName)

	return &RenderResult{
		Text:      text,
		ParseMode: "MarkdownV2",
	}
}

// renderSessionAttached renders a session attachment notification.
func (r *Renderer) renderSessionAttached(payload interface{}) *RenderResult {
	p, ok := payload.(router.SessionAttachedPayload)
	if !ok {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("📎 *Session attached*\n")
	sb.WriteString(EscapeMarkdownf("• Provider: `%s`\n", p.Provider))
	sb.WriteString(EscapeMarkdownf("• Model: `%s`", p.Model))

	return &RenderResult{
		Text:      sb.String(),
		ParseMode: "MarkdownV2",
	}
}

// renderModelChanged renders a model change notification.
func (r *Renderer) renderModelChanged(payload interface{}) *RenderResult {
	p, ok := payload.(router.ChangeProviderModelPayload)
	if !ok {
		return nil
	}

	text := EscapeMarkdownf("🔄 *Switched to* `%s` / `%s`", p.Provider, p.Model)

	return &RenderResult{
		Text:      text,
		ParseMode: "MarkdownV2",
	}
}

// SetStreamingMessageID sets the message ID for streaming delta updates.
func (r *Renderer) SetStreamingMessageID(id int64) {
	r.streamingMessageID = id
}

// GetStreamingMessageID returns the current streaming message ID.
func (r *Renderer) GetStreamingMessageID() int64 {
	return r.streamingMessageID
}

// ClearStreamingMessageID clears the streaming message ID.
func (r *Renderer) ClearStreamingMessageID() {
	r.streamingMessageID = 0
}

// EscapeMarkdown escapes text for Telegram's MarkdownV2 format.
func EscapeMarkdown(text string) string {
	// Characters that need escaping in MarkdownV2: _ * [ ] ( ) ~ ` > # + - = | { } . !
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
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
