package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"valinor/internal/applog"
	agentcontext "valinor/internal/context"
)

const (
	MetadataFile = "metadata.json"
	MessagesFile = "messages.json"
)

// JSONLStore implements Store using JSONL files on disk.
type JSONLStore struct {
	dir string
}

// NewJSONLStore opens or creates a session directory and returns a Store.
// The directory must already exist. Call Close when done.
func NewJSONLStore(dir string) (*JSONLStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		applog.Error("store create dir error", applog.M("dir", dir, "error", err))
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	return &JSONLStore{
		dir: dir,
	}, nil
}

// SaveMessages writes messages atomically (write to temp, then rename).
func (s *JSONLStore) SaveMessages(ctx context.Context, messages []agentcontext.Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		applog.Error("store save messages marshal error", applog.M("dir", s.dir, "error", err))
		return fmt.Errorf("marshal messages: %w", err)
	}

	tmpPath := filepath.Join(s.dir, MessagesFile+".tmp")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		applog.Error("store save messages write error", applog.M("dir", s.dir, "error", err))
		return fmt.Errorf("write messages: %w", err)
	}

	messagesPath := filepath.Join(s.dir, MessagesFile)
	if err := os.Rename(tmpPath, messagesPath); err != nil {
		_ = os.Remove(tmpPath)
		applog.Error("store save messages rename error", applog.M("dir", s.dir, "error", err))
		return fmt.Errorf("rename messages: %w", err)
	}
	return nil
}

// LoadMessages reads all persisted messages.
func (s *JSONLStore) LoadMessages(ctx context.Context) ([]agentcontext.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	messagesPath := filepath.Join(s.dir, MessagesFile)
	data, err := os.ReadFile(messagesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		applog.Error("store load messages read error", applog.M("dir", s.dir, "error", err))
		return nil, fmt.Errorf("read messages: %w", err)
	}

	var messages []agentcontext.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		applog.Error("store load messages unmarshal error", applog.M("dir", s.dir, "error", err))
		return nil, fmt.Errorf("unmarshal messages: %w", err)
	}
	return messages, nil
}

// SaveMetadata writes metadata atomically (write to temp, then rename).
func (s *JSONLStore) SaveMetadata(ctx context.Context, meta Metadata) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		applog.Error("store save metadata marshal error", applog.M("dir", s.dir, "session_id", meta.ID, "error", err))
		return fmt.Errorf("marshal metadata: %w", err)
	}

	tmpPath := filepath.Join(s.dir, MetadataFile+".tmp")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		applog.Error("store save metadata write error", applog.M("dir", s.dir, "session_id", meta.ID, "error", err))
		return fmt.Errorf("write metadata: %w", err)
	}

	metaPath := filepath.Join(s.dir, MetadataFile)
	if err := os.Rename(tmpPath, metaPath); err != nil {
		_ = os.Remove(tmpPath)
		applog.Error("store save metadata rename error", applog.M("dir", s.dir, "session_id", meta.ID, "error", err))
		return fmt.Errorf("rename metadata: %w", err)
	}
	return nil
}

// LoadMetadata reads metadata from metadata.json.
func (s *JSONLStore) LoadMetadata(ctx context.Context) (*Metadata, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	metaPath := filepath.Join(s.dir, MetadataFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		applog.Error("store load metadata read error", applog.M("dir", s.dir, "error", err))
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		applog.Error("store load metadata unmarshal error", applog.M("dir", s.dir, "error", err))
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return &meta, nil
}

// Close releases resources.
func (s *JSONLStore) Close() error {
	applog.Debug("store close success", applog.M("dir", s.dir))
	return nil
}
