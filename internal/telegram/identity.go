// Package telegram provides the Telegram Bot API long-polling adapter.
package telegram

import (
	"fmt"
	"strconv"
	"strings"
)

// Sequential key patterns for deterministic session resume and ordered processing.
// These patterns mirror the OpenClaw sequentialization design:
//   - DM: telegram:{chatId}
//   - Group/Forum topic: telegram:{chatId}:topic:{threadId}
//   - Control lane for cancel/stop commands: telegram:{chatId}:control
const (
	// KeyPrefix is the prefix for all sequential keys (DMs, groups, channels, forum topics).
	KeyPrefix = "telegram"
	// KeySegmentTopic is the segment identifier for topic/thread IDs.
	KeySegmentTopic = "topic"
	// KeySegmentControl is the segment identifier for control lane keys.
	KeySegmentControl = "control"
)

// IdentityType identifies the type of Telegram chat for routing purposes.
type IdentityType string

const (
	// IdentityTypeDM is a direct message chat.
	IdentityTypeDM IdentityType = "dm"
	// IdentityTypeGroup is a group chat.
	IdentityTypeGroup IdentityType = "group"
	// IdentityTypeChannel is a channel.
	IdentityTypeChannel IdentityType = "channel"
	// IdentityTypeForumTopic is a forum topic within a supergroup.
	IdentityTypeForumTopic IdentityType = "forum_topic"
)

// ChatIdentity uniquely identifies a Telegram chat or forum topic for session binding.
type ChatIdentity struct {
	ChatID   int64      `json:"chat_id"`   // Telegram chat ID
	ThreadID *int64     `json:"thread_id"` // Optional forum topic thread ID
	Type     IdentityType `json:"type"`      // Type of chat
	Username string     `json:"username,omitempty"` // Optional username for display
	Title    string     `json:"title,omitempty"`    // Optional chat title for display
}

// IsDirectMessage returns true if this is a DM (not a group/channel/topic).
func (ci ChatIdentity) IsDirectMessage() bool {
	return ci.Type == IdentityTypeDM && ci.ThreadID == nil
}

// IsForumTopic returns true if this identity represents a forum topic.
func (ci ChatIdentity) IsForumTopic() bool {
	return ci.ThreadID != nil
}

// ToClientIdentity converts ChatIdentity to router.ClientIdentity.
// The identity string is formatted as "{chatID}" or "{chatID}:topic:{threadID}".
func (ci ChatIdentity) ToClientIdentity() string {
	if ci.ThreadID != nil {
		return fmt.Sprintf("%d:%s:%d", ci.ChatID, KeySegmentTopic, *ci.ThreadID)
	}
	return strconv.FormatInt(ci.ChatID, 10)
}

// SequentialKey returns the key for per-chat sequential processing.
// This is used as the worker queue key to ensure ordered message handling.
func (ci ChatIdentity) SequentialKey() string {
	if ci.ThreadID != nil {
		return fmt.Sprintf("%s:%d:%s:%d", KeyPrefix, ci.ChatID, KeySegmentTopic, *ci.ThreadID)
	}
	return fmt.Sprintf("%s:%d", KeyPrefix, ci.ChatID)
}

// ControlLaneKey returns the control lane key for cancel/stop commands.
// Control commands use a separate lane to avoid blocking behind regular messages.
func (ci ChatIdentity) ControlLaneKey() string {
	return fmt.Sprintf("%s:%d:%s", KeyPrefix, ci.ChatID, KeySegmentControl)
}

// ParseChatIdentity parses a client identity string into ChatIdentity.
// Expected formats: "{chatID}" or "{chatID}:topic:{threadID}".
func ParseChatIdentity(identity string) (ChatIdentity, error) {
	// Try to parse as simple chat ID
	chatID, err := strconv.ParseInt(identity, 10, 64)
	if err == nil {
		return ChatIdentity{
			ChatID: chatID,
			Type:   IdentityTypeDM,
		}, nil
	}

	// Try to parse as "chatID:topic:threadID" format
	parts := strings.Split(identity, ":")
	if len(parts) == 3 && parts[1] == KeySegmentTopic {
		chatIDInt, err1 := strconv.ParseInt(parts[0], 10, 64)
		threadIDInt, err2 := strconv.ParseInt(parts[2], 10, 64)
		if err1 == nil && err2 == nil {
			return ChatIdentity{
				ChatID:   chatIDInt,
				ThreadID: &threadIDInt,
				Type:     IdentityTypeForumTopic,
			}, nil
		}
	}

	return ChatIdentity{}, fmt.Errorf("invalid chat identity format: %s", identity)
}

// FromTelegramUpdate extracts ChatIdentity from a Telegram update.
// The chat type is inferred from the Telegram chat object.
func FromTelegramUpdate(chat *Chat) ChatIdentity {
	if chat == nil {
		return ChatIdentity{}
	}

	ci := ChatIdentity{
		ChatID:   chat.ID,
		Username: chat.Username,
		Title:    chat.Title,
	}

	// Determine chat type from Telegram type
	switch chat.Type {
	case "private":
		ci.Type = IdentityTypeDM
	case "group":
		ci.Type = IdentityTypeGroup
	case "supergroup":
		ci.Type = IdentityTypeGroup
	case "channel":
		ci.Type = IdentityTypeChannel
	}

	return ci
}

// WithThreadID returns a new ChatIdentity with the specified thread ID set.
// This is used for forum topics where the thread ID comes from message_thread_id.
func (ci ChatIdentity) WithThreadID(threadID int64) ChatIdentity {
	return ChatIdentity{
		ChatID:   ci.ChatID,
		ThreadID: &threadID,
		Type:     IdentityTypeForumTopic,
		Username: ci.Username,
		Title:    ci.Title,
	}
}
