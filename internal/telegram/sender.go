// Package telegram provides Telegram API message sending.
package telegram

import (
	"encoding/base64"
	"fmt"
	"time"
	"unicode/utf8"

	"valinor/internal/logger"
)

const (
	typingActionInterval      = 4 * time.Second
	typingActionErrorCooldown = 2 * time.Second
	defaultTypingRetryAfter   = 3 * time.Second
)

// sendTelegramMessage sends a message via the Telegram API.
// Supports both sending new messages and editing existing ones for streaming updates.
func (b *Bot) sendTelegramMessage(identity ChatIdentity, tracker *messageTracker, result *RenderResult) error {
	if len(result.Images) > 0 {
		return b.sendPhotoMessages(identity, result)
	}

	if !result.IsStreaming && utf8.RuneCountInString(result.Text) > MaxTelegramMessageLength {
		return b.sendChunkedMessage(identity, tracker, result)
	}

	// Determine if we should edit or send a new message
	shouldEdit := result.IsStreaming && tracker.getStreamingMsgID() != 0

	if shouldEdit {
		if tracker.isSameStreamText(result.Text) {
			return nil
		}

		// Edit existing streaming message
		req := EditMessageTextRequest{
			ChatID:      identity.ChatID,
			MessageID:   tracker.getStreamingMsgID(),
			Text:        result.Text,
			ParseMode:   result.ParseMode,
			ReplyMarkup: result.ReplyMarkup,
		}

		_, err := b.client.EditMessageText(b.ctx, req)
		if shouldRetryWithPlainText(err, result) {
			req.Text = result.FallbackText
			req.ParseMode = ""
			_, err = b.client.EditMessageText(b.ctx, req)
		}
		if isNotModifiedError(err) {
			tracker.setLastStreamText(result.Text)
			return nil
		}
		if err != nil {
			if isRateLimitError(err) {
				return err
			}
			// If editing fails (e.g., message too old), fall back to sending new message
			logger.Warning("telegram bot: edit failed, sending new message",
				logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
			tracker.setStreamingMsgID(0)
			return b.sendNewMessage(identity, tracker, result)
		}

		tracker.setLastStreamText(result.Text)
		return nil
	}

	// Send new message
	return b.sendNewMessage(identity, tracker, result)
}

// sendChunkedMessage delivers a long response in numbered parts.
func (b *Bot) sendChunkedMessage(identity ChatIdentity, tracker *messageTracker, result *RenderResult) error {
	text := result.Text
	if result.ParseMode != "" && result.FallbackText != "" {
		text = result.FallbackText
	}

	parts := splitTelegramText(text, MaxTelegramMessageLength-20)
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		return b.sendNewMessage(identity, tracker, &RenderResult{
			Text:         parts[0],
			FallbackText: parts[0],
			IsFinal:      true,
		})
	}

	for idx, part := range parts {
		prefix := fmt.Sprintf("(%d/%d)\n\n", idx+1, len(parts))
		if err := b.sendNewMessage(identity, tracker, &RenderResult{
			Text:         prefix + part,
			FallbackText: prefix + part,
			IsFinal:      true,
		}); err != nil {
			return fmt.Errorf("send part %d/%d: %w", idx+1, len(parts), err)
		}
	}
	return nil
}

// sendNewMessage sends a new Telegram message.
// Sets the streaming message ID if this is the first message during streaming.
func (b *Bot) sendNewMessage(identity ChatIdentity, tracker *messageTracker, result *RenderResult) error {
	req := SendMessageRequest{
		ChatID:      identity.ChatID,
		Text:        result.Text,
		ParseMode:   result.ParseMode,
		ReplyMarkup: result.ReplyMarkup,
	}

	msg, err := b.client.SendMessage(b.ctx, req)
	if shouldRetryWithPlainText(err, result) {
		req.Text = result.FallbackText
		req.ParseMode = ""
		msg, err = b.client.SendMessage(b.ctx, req)
	}
	if err != nil {
		return fmt.Errorf("send message to chat %d: %w", identity.ChatID, err)
	}

	// Set as streaming message ID if we're in streaming mode and don't have one yet
	if result.IsStreaming && tracker.isBusy() && tracker.getStreamingMsgID() == 0 {
		tracker.setStreamingMsgID(msg.MessageID)
		tracker.setLastStreamText(result.Text)
	}

	logger.Debug("telegram bot: message sent",
		logger.BuildMetadata("chat_id", identity.ChatID, "message_id", msg.MessageID))

	return nil
}

func (b *Bot) sendPhotoMessages(identity ChatIdentity, result *RenderResult) error {
	for idx, image := range result.Images {
		photoData, err := base64.StdEncoding.DecodeString(image.Base64)
		if err != nil {
			return fmt.Errorf("decode photo payload: %w", err)
		}

		req := SendPhotoRequest{
			ChatID:      identity.ChatID,
			PhotoName:   image.FileName,
			PhotoData:   photoData,
			ReplyMarkup: result.ReplyMarkup,
		}
		if idx == 0 {
			req.Caption = telegramCaption(result.Text)
			req.ParseMode = result.ParseMode
		}

		msg, err := b.client.SendPhoto(b.ctx, req)
		if idx == 0 && shouldRetryWithPlainText(err, result) {
			req.Caption = telegramCaption(result.FallbackText)
			req.ParseMode = ""
			msg, err = b.client.SendPhoto(b.ctx, req)
		}
		if err != nil {
			return fmt.Errorf("send photo to chat %d: %w", identity.ChatID, err)
		}

		logger.Debug("telegram bot: photo sent",
			logger.BuildMetadata("chat_id", identity.ChatID, "message_id", msg.MessageID, "file_name", image.FileName))
	}

	return nil
}

// sendTypingAction updates the chat with Telegram's typing indicator.
// It uses per-chat pacing/backoff to avoid Telegram 429 rate limits.
func (b *Bot) sendTypingAction(identity ChatIdentity, tracker *messageTracker) error {
	now := time.Now()
	if !tracker.canSendTyping(now) {
		return nil
	}

	req := SendChatActionRequest{
		ChatID: identity.ChatID,
		Action: "typing",
	}

	if err := b.client.SendChatAction(b.ctx, req); err != nil {
		retryAfter := typingActionErrorCooldown
		if apiErr, ok := err.(*APIResponse); ok && apiErr.ErrorCode == 429 {
			retryAfter = defaultTypingRetryAfter
			if apiErr.Parameters != nil && apiErr.Parameters.RetryAfter > 0 {
				retryAfter = time.Duration(apiErr.Parameters.RetryAfter) * time.Second
			}
		}
		tracker.backoffTyping(now, retryAfter)
		return err
	}

	tracker.markTypingSent(now, typingActionInterval)
	return nil
}

func telegramCaption(text string) string {
	if utf8.RuneCountInString(text) > MaxTelegramCaptionLength {
		return truncateTelegramText(text, MaxTelegramCaptionLength)
	}
	return text
}

func shouldRetryWithPlainText(err error, result *RenderResult) bool {
	if err == nil || result == nil || result.ParseMode == "" || result.FallbackText == "" {
		return false
	}

	apiErr, ok := err.(*APIResponse)
	return ok && apiErr.IsParseError()
}

func isRateLimitError(err error) bool {
	apiErr, ok := err.(*APIResponse)
	return ok && apiErr.ErrorCode == 429
}

func isNotModifiedError(err error) bool {
	apiErr, ok := err.(*APIResponse)
	return ok && apiErr.IsNotModifiedError()
}
