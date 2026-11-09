package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	agentcontext "valinor/internal/context"
	"valinor/internal/logger"
)

const MessagesFile = "messages.json"

// FileStore implements Store using JSON files on disk.
type FileStore struct {
	dir          string
	sessionID    string
	metadataRepo MetadataRepository
	mu           sync.Mutex
	closed       bool
}

// SessionDir returns the absolute path to this session's storage directory.
func (s *FileStore) SessionDir() string {
	return s.dir
}

// NewFileStore opens or creates a session directory and returns a Store.
// The directory must already exist. Call Close when done.
func NewFileStore(dir string, metadataRepo MetadataRepository) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.Error("store create dir error", logger.BuildMetadata("dir", dir, "error", err))
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	return &FileStore{
		dir:          dir,
		sessionID:    filepath.Base(dir),
		metadataRepo: metadataRepo,
	}, nil
}

// SaveMessages writes messages atomically (write to temp, then rename).
func (s *FileStore) SaveMessages(ctx context.Context, messages []agentcontext.Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	persistMessages, err := externalizeMessages(s.dir, messages)
	if err != nil {
		logger.Error("store save messages externalize error", logger.BuildMetadata("dir", s.dir, "error", err))
		return fmt.Errorf("externalize messages: %w", err)
	}

	data, err := json.MarshalIndent(persistMessages, "", "  ")
	if err != nil {
		logger.Error("store save messages marshal error", logger.BuildMetadata("dir", s.dir, "error", err))
		return fmt.Errorf("marshal messages: %w", err)
	}

	tmpPath := filepath.Join(s.dir, MessagesFile+".tmp")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		logger.Error("store save messages write error", logger.BuildMetadata("dir", s.dir, "error", err))
		return fmt.Errorf("write messages: %w", err)
	}

	messagesPath := filepath.Join(s.dir, MessagesFile)
	if err := os.Rename(tmpPath, messagesPath); err != nil {
		_ = os.Remove(tmpPath)
		logger.Error("store save messages rename error", logger.BuildMetadata("dir", s.dir, "error", err))
		return fmt.Errorf("rename messages: %w", err)
	}
	return nil
}

// LoadMessages reads all persisted messages.
func (s *FileStore) LoadMessages(ctx context.Context) ([]agentcontext.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	messagesPath := filepath.Join(s.dir, MessagesFile)
	messages, err := readMessagesFile(messagesPath)
	if err != nil {
		logger.Error("store load messages error", logger.BuildMetadata("dir", s.dir, "error", err))
		return nil, err
	}
	return messages, nil
}

// readMessagesFile reads and decodes one session transcript file.
// A missing file is treated as an empty transcript.
func readMessagesFile(path string) ([]agentcontext.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read messages: %w", err)
	}

	var messages []agentcontext.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("unmarshal messages: %w", err)
	}
	return messages, nil
}

// SaveMetadata stores metadata in SQLite.
func (s *FileStore) SaveMetadata(ctx context.Context, meta Metadata) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if s.metadataRepo == nil {
		return fmt.Errorf("metadata repository is not configured")
	}
	if err := s.metadataRepo.Upsert(ctx, meta); err != nil {
		logger.Error("store save metadata error", logger.BuildMetadata("dir", s.dir, "session_id", meta.ID, "error", err))
		return fmt.Errorf("save metadata: %w", err)
	}
	return nil
}

// LoadMetadata reads metadata from SQLite.
func (s *FileStore) LoadMetadata(ctx context.Context) (*Metadata, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if s.metadataRepo == nil {
		return nil, fmt.Errorf("metadata repository is not configured")
	}
	meta, err := s.metadataRepo.GetByID(ctx, s.sessionID)
	if err != nil {
		logger.Error("store load metadata error", logger.BuildMetadata("dir", s.dir, "session_id", s.sessionID, "error", err))
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	return meta, nil
}

// Close releases resources. Safe to call multiple times.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	logger.Debug("store close success", logger.BuildMetadata("dir", s.dir))
	return nil
}
