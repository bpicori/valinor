package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	agentcontext "valinor/internal/context"
)

// TranscriptSummary describes one persisted session transcript for offline review.
type TranscriptSummary struct {
	Meta      Metadata
	UpdatedAt time.Time // transcript file modification time
	Bytes     int64
}

// ListTranscripts returns persisted sessions together with transcript metadata.
// Sessions whose transcript file is missing are skipped.
func (m *Manager) ListTranscripts(ctx context.Context) ([]TranscriptSummary, error) {
	metas, err := m.metadataStore.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list session metadata: %w", err)
	}

	out := make([]TranscriptSummary, 0, len(metas))
	for _, meta := range metas {
		path := filepath.Join(m.sessionDir, meta.ID, MessagesFile)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat transcript for session %s: %w", meta.ID, err)
		}
		out = append(out, TranscriptSummary{
			Meta:      meta,
			UpdatedAt: info.ModTime(),
			Bytes:     info.Size(),
		})
	}
	return out, nil
}

// LoadTranscript returns the persisted messages for one session.
func (m *Manager) LoadTranscript(ctx context.Context, sessionID string) ([]agentcontext.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if sessionID == "" || sessionID != filepath.Base(sessionID) {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	return readMessagesFile(filepath.Join(m.sessionDir, sessionID, MessagesFile))
}
