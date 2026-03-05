// Package telegram provides identity types for Telegram chat routing.
package telegram

import (
	"fmt"
	"strconv"
)

// ChatIdentity uniquely identifies a Telegram DM chat for session binding.
type ChatIdentity struct {
	ChatID   int64  `json:"chat_id"`            // Telegram chat ID
	Username string `json:"username,omitempty"` // Optional username for display
}

// ToClientIdentity converts ChatIdentity to gateway.ClientIdentity.
// The identity string is formatted as "{chatID}".
func (ci ChatIdentity) ToClientIdentity() string {
	return strconv.FormatInt(ci.ChatID, 10)
}

// ParseChatIdentity parses a client identity string into ChatIdentity.
// Expected format: "{chatID}".
func ParseChatIdentity(identity string) (ChatIdentity, error) {
	chatID, err := strconv.ParseInt(identity, 10, 64)
	if err != nil {
		return ChatIdentity{}, fmt.Errorf("invalid chat identity format: %s", identity)
	}
	return ChatIdentity{ChatID: chatID}, nil
}

// FromTelegramUpdate extracts ChatIdentity from a Telegram update.
func FromTelegramUpdate(chat *Chat) ChatIdentity {
	if chat == nil {
		return ChatIdentity{}
	}
	return ChatIdentity{
		ChatID:   chat.ID,
		Username: chat.Username,
	}
}
