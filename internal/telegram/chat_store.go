// Package telegram provides persistent storage for known chats.
package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"valinor/internal/logger"
)

const knownChatsFileName = "telegram_known_chats.json"

// KnownChat represents a chat that has messaged the bot.
type KnownChat struct {
	ChatID    int64      `json:"chat_id"`
	ThreadID  *int64     `json:"thread_id,omitempty"`
	Type      string     `json:"type"`
	Username  string     `json:"username,omitempty"`
	Title     string     `json:"title,omitempty"`
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
}

// ToChatIdentity converts a KnownChat to ChatIdentity.
func (kc *KnownChat) ToChatIdentity() ChatIdentity {
	ci := ChatIdentity{
		ChatID:   kc.ChatID,
		ThreadID: kc.ThreadID,
		Username: kc.Username,
		Title:    kc.Title,
	}
	// Restore type from string
	switch kc.Type {
	case "private":
		ci.Type = IdentityTypeDM
	case "group", "supergroup":
		ci.Type = IdentityTypeGroup
	case "channel":
		ci.Type = IdentityTypeChannel
	default:
		ci.Type = IdentityTypeDM
	}
	return ci
}

// knownChatsData is the JSON structure for storage.
type knownChatsData struct {
	Chats []KnownChat `json:"chats"`
}

// KnownChatStore persists chat identities that have messaged the bot.
// This allows pre-creating bridges on startup so cron results are delivered
// to all known chats, not just currently active ones.
type KnownChatStore struct {
	path string
	mu   sync.RWMutex
	data knownChatsData
}

// newKnownChatStore creates a new known chats store in the specified directory.
func newKnownChatStore(dir string) (*KnownChatStore, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create known chats store directory: %w", err)
	}

	store := &KnownChatStore{
		path: filepath.Join(dir, knownChatsFileName),
	}

	// Load existing data if present
	if err := store.load(); err != nil {
		logger.Warning("telegram known chats store: failed to load, starting fresh",
			logger.BuildMetadata("error", err))
	}

	return store, nil
}

// Add records a chat that has messaged the bot.
// If the chat already exists, updates the LastSeen timestamp.
func (s *KnownChatStore) Add(identity ChatIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()

	// Check if already exists
	for i, chat := range s.data.Chats {
		if chat.ChatID == identity.ChatID && ptrInt64Equal(chat.ThreadID, identity.ThreadID) {
			// Update last seen
			s.data.Chats[i].LastSeen = now
			return s.saveLocked()
		}
	}

	// Add new chat
	chatType := "private"
	switch identity.Type {
	case IdentityTypeDM:
		chatType = "private"
	case IdentityTypeGroup:
		chatType = "group"
	case IdentityTypeChannel:
		chatType = "channel"
	case IdentityTypeForumTopic:
		chatType = "forum_topic"
	}

	s.data.Chats = append(s.data.Chats, KnownChat{
		ChatID:    identity.ChatID,
		ThreadID:  identity.ThreadID,
		Type:      chatType,
		Username:  identity.Username,
		Title:     identity.Title,
		FirstSeen: now,
		LastSeen:  now,
	})

	logger.Info("telegram: new chat recorded",
		logger.BuildMetadata("chat_id", identity.ChatID, "thread_id", identity.ThreadID))

	return s.saveLocked()
}

// List returns all known chats.
func (s *KnownChatStore) List() []KnownChat {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]KnownChat, len(s.data.Chats))
	copy(result, s.data.Chats)
	return result
}

// Count returns the number of known chats.
func (s *KnownChatStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data.Chats)
}

// load reads the known chats data from disk.
func (s *KnownChatStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing file is fine
		}
		return fmt.Errorf("read known chats file: %w", err)
	}

	var loaded knownChatsData
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("parse known chats file: %w", err)
	}

	s.data = loaded
	logger.Info("telegram: known chats loaded",
		logger.BuildMetadata("count", len(s.data.Chats)))
	return nil
}

// saveLocked persists the known chats to disk.
// Must be called with s.mu held.
func (s *KnownChatStore) saveLocked() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal known chats data: %w", err)
	}

	// Write to temp file first, then rename for atomicity
	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0640); err != nil {
		return fmt.Errorf("write known chats temp file: %w", err)
	}

	if err := os.Rename(tempPath, s.path); err != nil {
		os.Remove(tempPath) // Clean up on failure
		return fmt.Errorf("rename known chats file: %w", err)
	}

	return nil
}

// ptrInt64Equal compares two optional int64 pointers for equality.
func ptrInt64Equal(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
