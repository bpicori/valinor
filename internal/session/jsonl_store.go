package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"valinor/internal/applog"
)

const (
	MetadataFile   = "metadata.json"
	TranscriptFile = "transcript.jsonl"
)

// JSONLStore implements Store using JSONL files on disk.
type JSONLStore struct {
	dir        string
	transcript *os.File
}

// NewJSONLStore opens or creates a session directory and returns a Store.
// The directory must already exist. Call Close when done.
func NewJSONLStore(dir string) (*JSONLStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		applog.Error("store create dir error", applog.M("dir", dir, "error", err))
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	transcriptPath := filepath.Join(dir, TranscriptFile)
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		applog.Error("store create transcript error", applog.M("dir", dir, "error", err))
		return nil, fmt.Errorf("open transcript: %w", err)
	}

	return &JSONLStore{
		dir:        dir,
		transcript: f,
	}, nil
}

// AppendEvent appends a single event as a JSON line to transcript.jsonl.
func (s *JSONLStore) AppendEvent(ctx context.Context, evt StoredEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	data, err := json.Marshal(evt)
	if err != nil {
		applog.Error("store append marshal error", applog.M("dir", s.dir, "event_type", evt.Type, "error", err))
		return fmt.Errorf("marshal event: %w", err)
	}
	data = append(data, '\n')

	_, err = s.transcript.Write(data)
	if err != nil {
		applog.Error("store append write error", applog.M("dir", s.dir, "event_type", evt.Type, "error", err))
		return fmt.Errorf("write transcript: %w", err)
	}
	return nil
}

// LoadEvents reads all events from transcript.jsonl in order.
func (s *JSONLStore) LoadEvents(ctx context.Context) ([]StoredEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	transcriptPath := filepath.Join(s.dir, TranscriptFile)
	f, err := os.Open(transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		applog.Error("store load open error", applog.M("dir", s.dir, "error", err))
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	var events []StoredEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt StoredEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			applog.Error("store load unmarshal error", applog.M("dir", s.dir, "error", err))
			return nil, fmt.Errorf("unmarshal event: %w", err)
		}
		events = append(events, evt)
	}
	if err := scanner.Err(); err != nil {
		applog.Error("store load scanner error", applog.M("dir", s.dir, "error", err))
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	return events, nil
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
	if s.transcript != nil {
		dir := s.dir
		err := s.transcript.Close()
		s.transcript = nil
		if err != nil {
			applog.Error("store close error", applog.M("dir", dir, "error", err))
			return err
		}
		applog.Debug("store close success", applog.M("dir", dir))
	}
	return nil
}
