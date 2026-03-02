package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"valinor/internal/logger"
)

const offsetFileName = "telegram_offset.json"

// offsetStore persists and retrieves the last processed update_id.
// This ensures we don't process the same updates multiple times after restarts.
type offsetStore struct {
	path string
	mu   sync.RWMutex
	data offsetData
}

type offsetData struct {
	LastUpdateID int64 `json:"last_update_id"`
}

// newOffsetStore creates a new offset store in the specified directory.
func newOffsetStore(dir string) (*offsetStore, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create offset store directory: %w", err)
	}

	store := &offsetStore{
		path: filepath.Join(dir, offsetFileName),
	}

	// Load existing data if present
	if err := store.load(); err != nil {
		logger.Warning("telegram offset store: failed to load, starting fresh",
			logger.BuildMetadata("error", err))
	}

	return store, nil
}

// Get returns the last processed update ID.
// Returns 0 if no offset has been stored.
func (s *offsetStore) Get() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.LastUpdateID
}

// Set updates the last processed update ID and persists it to disk.
func (s *offsetStore) Set(updateID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Only update if the new ID is higher
	if updateID <= s.data.LastUpdateID {
		return nil
	}

	s.data.LastUpdateID = updateID
	return s.saveLocked()
}

// load reads the offset data from disk.
func (s *offsetStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing file is fine
		}
		return fmt.Errorf("read offset file: %w", err)
	}

	var loaded offsetData
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("parse offset file: %w", err)
	}

	s.data = loaded
	return nil
}

// saveLocked persists the current offset to disk.
// Must be called with s.mu held.
func (s *offsetStore) saveLocked() error {
	data, err := json.Marshal(s.data)
	if err != nil {
		return fmt.Errorf("marshal offset data: %w", err)
	}

	// Write to temp file first, then rename for atomicity
	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0640); err != nil {
		return fmt.Errorf("write offset temp file: %w", err)
	}

	if err := os.Rename(tempPath, s.path); err != nil {
		os.Remove(tempPath) // Clean up on failure
		return fmt.Errorf("rename offset file: %w", err)
	}

	return nil
}
