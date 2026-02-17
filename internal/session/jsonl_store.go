package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	MetadataFile   = "metadata.json"
	TranscriptFile = "transcript.jsonl"
)

// JSONLStore implements Store using JSONL files on disk.
type JSONLStore struct {
	dir         string
	transcript  *os.File
}

// NewJSONLStore opens or creates a session directory and returns a Store.
// The directory must already exist. Call Close when done.
func NewJSONLStore(dir string) (*JSONLStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	transcriptPath := filepath.Join(dir, TranscriptFile)
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
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
		return fmt.Errorf("marshal event: %w", err)
	}
	data = append(data, '\n')

	_, err = s.transcript.Write(data)
	if err != nil {
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
			return nil, fmt.Errorf("unmarshal event: %w", err)
		}
		events = append(events, evt)
	}
	if err := scanner.Err(); err != nil {
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
		return fmt.Errorf("marshal metadata: %w", err)
	}

	tmpPath := filepath.Join(s.dir, MetadataFile+".tmp")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	metaPath := filepath.Join(s.dir, MetadataFile)
	if err := os.Rename(tmpPath, metaPath); err != nil {
		_ = os.Remove(tmpPath)
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
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return &meta, nil
}

// Close releases resources.
func (s *JSONLStore) Close() error {
	if s.transcript != nil {
		err := s.transcript.Close()
		s.transcript = nil
		return err
	}
	return nil
}
