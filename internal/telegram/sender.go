// Package telegram provides Telegram API message sending.
package telegram

import (
	"fmt"
	"time"

	"valinor/internal/logger"
)

const (
	typingActionInterval      = 4 * time.Second
	typingActionErrorCooldown = 2 * time.Second
	defaultTypingRetryAfter   = 3 * time.Second
)

// sendTelegramMessage sends a message via the Telegram API.
// Supports both sending new messages and editing existing ones for streaming updates.
// This now uses ChatState for determining edit vs new message (TUI-like lastMessage pattern).
func (b *Bot) sendTelegramMessage(identity ChatIdentity, state *ChatState, result *RenderResult) error {
	// Determine if we should edit or send a new message
	// TUI-like pattern: edit the streaming message if we're in streaming mode
	shouldEdit := result.IsStreaming && state.GetStreamingMessageID() != 0

	if shouldEdit {
		// Edit existing streaming message
		req := EditMessageTextRequest{
			ChatID:      identity.ChatID,
			MessageID:   state.GetStreamingMessageID(),
			Text:        result.Text,
			ParseMode:   result.ParseMode,
			ReplyMarkup: result.ReplyMarkup,
		}

		// Handle thread ID for forum topics
		if identity.ThreadID != nil {
			logger.Debug("telegram bot: editing message in thread",
				logger.BuildMetadata("chat_id", identity.ChatID, "thread_id", *identity.ThreadID, "message_id", state.GetStreamingMessageID()))
		}

		_, err := b.client.EditMessageText(b.ctx, req)
		if err != nil {
			// If editing fails (e.g., message too old), fall back to sending new message
			logger.Warning("telegram bot: edit failed, sending new message",
				logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
			return b.sendNewMessage(identity, state, result)
		}

		return nil
	}

	// Send new message
	return b.sendNewMessage(identity, state, result)
}

// sendNewMessage sends a new Telegram message.
// Sets the streaming message ID if this is the first message during streaming.
func (b *Bot) sendNewMessage(identity ChatIdentity, state *ChatState, result *RenderResult) error {
	req := SendMessageRequest{
		ChatID:      identity.ChatID,
		Text:        result.Text,
		ParseMode:   result.ParseMode,
		ReplyMarkup: result.ReplyMarkup,
	}

	// Handle thread ID for forum topics
	if identity.ThreadID != nil {
		req.MessageThreadID = identity.ThreadID
	}

	msg, err := b.client.SendMessage(b.ctx, req)
	if err != nil {
		return fmt.Errorf("send message to chat %d: %w", identity.ChatID, err)
	}

	// Update state with new message ID
	state.UpdateLastMessageID(msg.MessageID)

	// Set as streaming message ID if we're in streaming mode and don't have one yet
	// This ensures subsequent deltas edit this message (TUI-like pattern)
	if result.IsStreaming && state.GetStreamingMessageID() == 0 {
		state.SetStreamingMessageID(msg.MessageID)
	}

	logger.Debug("telegram bot: message sent",
		logger.BuildMetadata("chat_id", identity.ChatID, "message_id", msg.MessageID))

	return nil
}

// sendTypingAction updates the chat with Telegram's typing indicator.
// It uses per-chat pacing/backoff to avoid Telegram 429 rate limits.
func (b *Bot) sendTypingAction(identity ChatIdentity, state *ChatState) error {
	now := time.Now()
	if !state.CanSendTypingAction(now) {
		return nil
	}

	req := SendChatActionRequest{
		ChatID: identity.ChatID,
		Action: "typing",
	}
	if identity.ThreadID != nil {
		req.MessageThreadID = identity.ThreadID
	}

	if err := b.client.SendChatAction(b.ctx, req); err != nil {
		retryAfter := typingActionErrorCooldown
		if apiErr, ok := err.(*APIResponse); ok && apiErr.ErrorCode == 429 {
			retryAfter = defaultTypingRetryAfter
			if apiErr.Parameters != nil && apiErr.Parameters.RetryAfter > 0 {
				retryAfter = time.Duration(apiErr.Parameters.RetryAfter) * time.Second
			}
		}
		state.BackoffTypingAction(now, retryAfter)
		return err
	}

	state.MarkTypingActionSent(now, typingActionInterval)
	return nil
}
