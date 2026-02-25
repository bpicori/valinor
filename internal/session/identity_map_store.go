// Package session provides session management and identity mapping.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"valinor/internal/applog"
)

// IdentityMapping maps a client identity key to a session ID.
type IdentityMapping struct {
	IdentityKey string    `json:"identity_key"`
	SessionID   string    `json:"session_id"`
	ClientType  string    `json:"client_type"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// IdentityMapStore persists identity-to-session mappings.
// It stores mappings in a JSON file in the valinor directory.
type IdentityMapStore struct {
	path string
	mu   sync.RWMutex
	data map[string]IdentityMapping // key: identity_key
}

// NewIdentityMapStore creates a new identity map store.
// The store file is located at baseDir/identity_mappings.json.
func NewIdentityMapStore(baseDir string) (*IdentityMapStore, error) {
	store := &IdentityMapStore{
		path: filepath.Join(baseDir, "identity_mappings.json"),
		data: make(map[string]IdentityMapping),
	}

	// Load existing data if file exists
	if err := store.load(); err != nil {
		// If file doesn't exist, that's OK - we'll create it on first save
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("load identity mappings: %w", err)
		}
	}

	return store, nil
}

// Get retrieves a mapping by identity key.
// Returns nil if no mapping exists.
func (s *IdentityMapStore) Get(identityKey string) *IdentityMapping {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if mapping, ok := s.data[identityKey]; ok {
		return &mapping
	}
	return nil
}

// GetBySessionID retrieves all mappings for a given session ID.
func (s *IdentityMapStore) GetBySessionID(sessionID string) []IdentityMapping {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []IdentityMapping
	for _, mapping := range s.data {
		if mapping.SessionID == sessionID {
			result = append(result, mapping)
		}
	}
	return result
}

// Set creates or updates a mapping.
func (s *IdentityMapStore) Set(identityKey, sessionID, clientType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	if existing, ok := s.data[identityKey]; ok {
		// Update existing
		existing.SessionID = sessionID
		existing.UpdatedAt = now
		s.data[identityKey] = existing
	} else {
		// Create new
		s.data[identityKey] = IdentityMapping{
			IdentityKey: identityKey,
			SessionID:   sessionID,
			ClientType:  clientType,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
	}

	// Persist
	if err := s.saveLocked(); err != nil {
		// Rollback on save error
		delete(s.data, identityKey)
		return fmt.Errorf("save identity mapping: %w", err)
	}

	applog.Info("identity mapping saved",
		applog.BuildMetadata("identity_key", identityKey, "session_id", sessionID))

	return nil
}

// Remove deletes a mapping by identity key.
func (s *IdentityMapStore) Remove(identityKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.data[identityKey]; !ok {
		return nil // Already removed
	}

	delete(s.data, identityKey)

	if err := s.saveLocked(); err != nil {
		return fmt.Errorf("save after remove: %w", err)
	}

	applog.Info("identity mapping removed", applog.BuildMetadata("identity_key", identityKey))

	return nil
}

// RemoveBySessionID removes all mappings for a given session ID.
func (s *IdentityMapStore) RemoveBySessionID(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for key, mapping := range s.data {
		if mapping.SessionID == sessionID {
			delete(s.data, key)
			changed = true
		}
	}

	if !changed {
		return nil
	}

	if err := s.saveLocked(); err != nil {
		return fmt.Errorf("save after session removal: %w", err)
	}

	applog.Info("identity mappings removed for session", applog.BuildMetadata("session_id", sessionID))

	return nil
}

// List returns all mappings.
func (s *IdentityMapStore) List() []IdentityMapping {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]IdentityMapping, 0, len(s.data))
	for _, mapping := range s.data {
		result = append(result, mapping)
	}
	return result
}

// Count returns the number of mappings.
func (s *IdentityMapStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// load reads the mappings from disk.
func (s *IdentityMapStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}

	var mappings []IdentityMapping
	if err := json.Unmarshal(data, &mappings); err != nil {
		return fmt.Errorf("unmarshal mappings: %w", err)
	}

	s.data = make(map[string]IdentityMapping, len(mappings))
	for _, m := range mappings {
		s.data[m.IdentityKey] = m
	}

	applog.Info("identity mappings loaded",
		applog.BuildMetadata("count", len(s.data), "path", s.path))

	return nil
}

// saveLocked persists mappings to disk. Must be called with lock held.
func (s *IdentityMapStore) saveLocked() error {
	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Convert map to slice for stable ordering
	mappings := make([]IdentityMapping, 0, len(s.data))
	for _, m := range s.data {
		mappings = append(mappings, m)
	}

	// Marshal with indentation for readability
	data, err := json.MarshalIndent(mappings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mappings: %w", err)
	}

	// Write atomically using temp file
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// Close is a no-op for file-based store but implements a potential interface.
func (s *IdentityMapStore) Close() error {
	return nil
}
