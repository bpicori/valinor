// Package telegram provides Telegram Bot API types for update parsing.
package telegram

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Update represents a Telegram update received via getUpdates.
// See: https://core.telegram.org/bots/api#update
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	ChannelPost   *Message       `json:"channel_post,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message represents a Telegram message.
// See: https://core.telegram.org/bots/api#message
type Message struct {
	MessageID       int64           `json:"message_id"`
	MessageThreadID *int64          `json:"message_thread_id,omitempty"`
	From            *User           `json:"from,omitempty"`
	Date            int64           `json:"date"`
	Chat            *Chat           `json:"chat"`
	Text            string          `json:"text,omitempty"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`
}

// IsCommand returns true if the message has a bot command entity at the start.
func (m *Message) IsCommand() bool {
	if m == nil || len(m.Entities) == 0 || len(m.Text) == 0 {
		return false
	}
	// Check if first entity is a bot_command at offset 0
	if m.Entities[0].Offset == 0 && m.Entities[0].Type == "bot_command" {
		return true
	}
	return false
}

// CommandWithArgs returns the command and arguments separately.
func (m *Message) CommandWithArgs() (cmd, args string) {
	if !m.IsCommand() {
		return "", m.Text
	}
	entity := m.Entities[0]
	fullCmd := m.Text[entity.Offset : entity.Offset+entity.Length]

	// Extract command without / and @botname
	if len(fullCmd) > 0 && fullCmd[0] == '/' {
		fullCmd = fullCmd[1:]
	}
	for i, c := range fullCmd {
		if c == '@' {
			fullCmd = fullCmd[:i]
			break
		}
	}

	// Arguments start after the command entity
	if int(entity.Offset+entity.Length) < len(m.Text) {
		args = m.Text[entity.Offset+entity.Length:]
		// Trim leading space
		if len(args) > 0 && args[0] == ' ' {
			args = args[1:]
		}
	}

	return fullCmd, args
}

// User represents a Telegram user.
// See: https://core.telegram.org/bots/api#user
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

// Chat represents a Telegram chat.
// See: https://core.telegram.org/bots/api#chat
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"` // private, group, supergroup, channel
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	IsForum   bool   `json:"is_forum,omitempty"`
}

// MessageEntity represents a special entity in a message text.
// See: https://core.telegram.org/bots/api#messageentity
type MessageEntity struct {
	Type   string `json:"type"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
	URL    string `json:"url,omitempty"`
	User   *User  `json:"user,omitempty"`
}

// CallbackQuery represents a callback query from an inline keyboard.
// See: https://core.telegram.org/bots/api#callbackquery
type CallbackQuery struct {
	ID              string   `json:"id"`
	From            *User    `json:"from"`
	Message         *Message `json:"message,omitempty"`
	InlineMessageID string   `json:"inline_message_id,omitempty"`
	ChatInstance    string   `json:"chat_instance"`
	Data            string   `json:"data,omitempty"`
}

// SendMessageRequest represents the payload for sendMessage API.
type SendMessageRequest struct {
	ChatID          int64                 `json:"chat_id"`
	MessageThreadID *int64                `json:"message_thread_id,omitempty"`
	Text            string                `json:"text"`
	ParseMode       string                `json:"parse_mode,omitempty"`
	ReplyParameters *ReplyParameters      `json:"reply_parameters,omitempty"`
	ReplyMarkup     *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// SendPhotoRequest represents the parameters for sendPhoto.
type SendPhotoRequest struct {
	ChatID          int64
	MessageThreadID *int64
	PhotoName       string
	PhotoData       []byte
	Caption         string
	ParseMode       string
	ReplyParameters *ReplyParameters
	ReplyMarkup     *InlineKeyboardMarkup
}

// EditMessageTextRequest represents the payload for editMessageText API.
type EditMessageTextRequest struct {
	ChatID          int64                 `json:"chat_id,omitempty"`
	MessageID       int64                 `json:"message_id,omitempty"`
	InlineMessageID string                `json:"inline_message_id,omitempty"`
	Text            string                `json:"text"`
	ParseMode       string                `json:"parse_mode,omitempty"`
	ReplyMarkup     *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// SendChatActionRequest represents the payload for sendChatAction API.
type SendChatActionRequest struct {
	ChatID          int64  `json:"chat_id"`
	MessageThreadID *int64 `json:"message_thread_id,omitempty"`
	Action          string `json:"action"`
}

// ReplyParameters describes the message to reply to.
type ReplyParameters struct {
	MessageID                int64  `json:"message_id"`
	ChatID                   *int64 `json:"chat_id,omitempty"`
	AllowSendingWithoutReply bool   `json:"allow_sending_without_reply,omitempty"`
}

// InlineKeyboardMarkup represents an inline keyboard.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// InlineKeyboardButton represents one button of an inline keyboard.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// APIResponse is the standard response from Telegram Bot API.
type APIResponse struct {
	OK          bool                `json:"ok"`
	Result      json.RawMessage     `json:"result,omitempty"`
	ErrorCode   int                 `json:"error_code,omitempty"`
	Description string              `json:"description,omitempty"`
	Parameters  *ResponseParameters `json:"parameters,omitempty"`
}

// ResponseParameters contains information about why a request was unsuccessful.
type ResponseParameters struct {
	MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
	RetryAfter      int   `json:"retry_after,omitempty"`
}

// IsRecoverableError returns true if the error is transient and the request can be retried.
func (r *APIResponse) IsRecoverableError() bool {
	if r.OK {
		return false
	}
	// Rate limiting - can retry after RetryAfter seconds
	if r.ErrorCode == 429 {
		return true
	}
	// Bad Gateway, Gateway Timeout, Service Unavailable - transient network/server errors
	if r.ErrorCode == 502 || r.ErrorCode == 503 || r.ErrorCode == 504 {
		return true
	}
	// Internal Server Error - often transient
	if r.ErrorCode == 500 {
		return true
	}
	return false
}

// IsParseError returns true when Telegram rejected the payload because it could
// not parse the provided markup entities.
func (r *APIResponse) IsParseError() bool {
	if r == nil || r.OK || r.ErrorCode != 400 {
		return false
	}

	description := strings.ToLower(r.Description)
	return strings.Contains(description, "can't parse entities")
}

// Error implements the error interface.
func (r *APIResponse) Error() string {
	if r.OK {
		return ""
	}
	return fmt.Sprintf("Telegram API error %d: %s", r.ErrorCode, r.Description)
}

// GetUpdatesRequest represents the parameters for getUpdates.
type GetUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}
