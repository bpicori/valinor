// Package telegram provides persistent storage for the last processed update ID.
package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"valinor/internal/applog"
)

const offsetFileName = "telegram_offset.json"

// OffsetStore persists and retrieves the last processed update_id.
// This ensures we don't process the same updates multiple times after restarts.
type OffsetStore struct {
	path string
	mu   sync.RWMutex
	data offsetData
}

type offsetData struct {
	LastUpdateID int64 `json:"last_update_id"`
}

// NewOffsetStore creates a new offset store in the specified directory.
func NewOffsetStore(dir string) (*OffsetStore, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create offset store directory: %w", err)
	}

	store := &OffsetStore{
		path: filepath.Join(dir, offsetFileName),
	}

	// Load existing data if present
	if err := store.load(); err != nil {
		applog.Warning("telegram offset store: failed to load, starting fresh",
			applog.BuildMetadata("error", err))
	}

	return store, nil
}

// Get returns the last processed update ID.
// Returns 0 if no offset has been stored.
func (s *OffsetStore) Get() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.LastUpdateID
}

// Set updates the last processed update ID and persists it to disk.
func (s *OffsetStore) Set(updateID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Only update if the new ID is higher
	if updateID <= s.data.LastUpdateID {
		return nil
	}

	s.data.LastUpdateID = updateID
	return s.saveLocked()
}

// Clear resets the stored offset (useful for testing or manual recovery).
func (s *OffsetStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.LastUpdateID = 0
	return s.saveLocked()
}

// load reads the offset data from disk.
func (s *OffsetStore) load() error {
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
func (s *OffsetStore) saveLocked() error {
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
