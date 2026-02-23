package session

import (
	"context"

	agentcontext "valinor/internal/context"
)

// Store is the interface for session persistence backends.
type Store interface {
	SaveMessages(ctx context.Context, messages []agentcontext.Message) error
	LoadMessages(ctx context.Context) ([]agentcontext.Message, error)
	SaveMetadata(ctx context.Context, meta Metadata) error
	LoadMetadata(ctx context.Context) (*Metadata, error)
	Close() error
}
