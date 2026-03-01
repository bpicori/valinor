package session

import (
	"context"
	"sync"

	"valinor/internal/event"
	"valinor/internal/event_bus"
	"valinor/internal/logger"
)

// sessionWorker owns the execution state for a single session.
// Each worker processes commands sequentially for its session only.
type sessionWorker struct {
	session *Session

	mu            sync.Mutex
	runCancel     context.CancelFunc
	runInProgress bool
}

// newSessionWorker creates a new worker for the given session.
func newSessionWorker(sess *Session) *sessionWorker {
	return &sessionWorker{
		session: sess,
	}
}

// handleUserMessage processes a user message for this session.
// It manages its own run cancellation state independently.
func (w *sessionWorker) handleUserMessage(ctx context.Context, requestID string, content string, bus *event_bus.Bus) error {
	w.mu.Lock()
	// Cancel any existing run before starting a new one
	if w.runCancel != nil {
		w.runCancel()
	}

	runCtx, runCancel := context.WithCancel(ctx)
	w.runCancel = runCancel
	w.runInProgress = true
	w.mu.Unlock()

	// Execute the run
	err := w.session.HandleUserMessage(runCtx, requestID, content)

	w.mu.Lock()
	w.runCancel = nil
	w.runInProgress = false
	w.mu.Unlock()

	if err != nil {
		if runCtx.Err() == context.Canceled {
			logger.Info("session worker user message cancelled", logger.BuildMetadata("session_id", w.session.Meta().ID))
			bus.Publish(event.Event{
				Type:      event.AgentCancelled,
				Payload:   event.AgentCancelledPayload{},
				SessionID: w.session.Meta().ID,
				RequestID: requestID,
			})
			return nil
		}
		return err
	}

	return nil
}

// cancelRun cancels any in-progress run for this session.
func (w *sessionWorker) cancelRun() {
	w.mu.Lock()
	if w.runCancel != nil {
		w.runCancel()
		w.runCancel = nil
	}
	w.mu.Unlock()
}

// close cancels any in-progress run for this worker.
func (w *sessionWorker) close() {
	w.mu.Lock()
	if w.runCancel != nil {
		w.runCancel()
		w.runCancel = nil
	}
	w.mu.Unlock()
}
