package session

import (
	"context"
	"encoding/json"
	"time"
)

// StoredEventType identifies the kind of persisted event.
const (
	StoredEventUserMessage      = "user_message"
	StoredEventAssistantContent = "assistant_content"
	StoredEventToolCall         = "tool_call"
	StoredEventToolResult       = "tool_result"
	StoredEventAgentDone        = "agent_done"
)

// StoredEvent is a serializable representation of a conversation turn
// for persistence. Each line in transcript.jsonl is one StoredEvent.
type StoredEvent struct {
	Timestamp time.Time       `json:"ts"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
}

// Store is the interface for session persistence backends.
type Store interface {
	AppendEvent(ctx context.Context, evt StoredEvent) error
	LoadEvents(ctx context.Context) ([]StoredEvent, error)
	SaveMetadata(ctx context.Context, meta Metadata) error
	LoadMetadata(ctx context.Context) (*Metadata, error)
	Close() error
}
