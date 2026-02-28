package session

import (
	"context"
	"sync"

	agentcontext "valinor/internal/context"
)

// MemoryStore implements Store using in-memory storage (no persistence).
// Used for temporary sessions that don't need to survive process restart.
type MemoryStore struct {
	meta     *Metadata
	messages []agentcontext.Message
	mu       sync.RWMutex
}

// NewMemoryStore creates a new in-memory store for temporary sessions.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		messages: make([]agentcontext.Message, 0),
	}
}

// SaveMessages stores messages in memory.
func (s *MemoryStore) SaveMessages(ctx context.Context, messages []agentcontext.Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = agentcontext.CloneMessages(messages)
	return nil
}

// LoadMessages returns the stored messages from memory.
func (s *MemoryStore) LoadMessages(ctx context.Context) ([]agentcontext.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return agentcontext.CloneMessages(s.messages), nil
}

// SaveMetadata stores metadata in memory.
func (s *MemoryStore) SaveMetadata(ctx context.Context, meta Metadata) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	metaCopy := meta
	s.meta = &metaCopy
	return nil
}

// LoadMetadata returns the stored metadata from memory.
func (s *MemoryStore) LoadMetadata(ctx context.Context) (*Metadata, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.meta == nil {
		return nil, nil
	}
	metaCopy := *s.meta
	return &metaCopy, nil
}

// Close releases resources. No-op for memory store.
func (s *MemoryStore) Close() error {
	return nil
}
