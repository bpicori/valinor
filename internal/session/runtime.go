package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"valinor/internal/config"
	"valinor/internal/event"
	"valinor/internal/logger"
)

// EventEmitter is a callback for emitting events with session context.
// The router provides this to route events to the correct consumers.
type EventEmitter func(sessionID string, ev event.Event)

// Runtime owns the session worker registry and handles command dispatch.
// Commands are dispatched directly via HandleCommandForSession rather than through bus events.
type Runtime struct {
	ctx     context.Context
	manager *Manager

	mu sync.RWMutex
	workers       map[string]*sessionWorker // sessionID -> worker
	runtimeCtx    context.Context
	runtimeCancel context.CancelFunc
	started       bool
	closed        bool
	wg            sync.WaitGroup
}

// NewRuntime creates a session runtime with a worker registry.
func NewRuntime(ctx context.Context, manager *Manager) *Runtime {
	return &Runtime{
		ctx:     ctx,
		manager: manager,
		workers: make(map[string]*sessionWorker),
	}
}

// Start starts the runtime.
func (r *Runtime) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || r.started {
		return
	}
	r.started = true
	r.runtimeCtx, r.runtimeCancel = context.WithCancel(r.ctx)
}

// RegisterSession registers a session with the runtime, creating a worker if needed.
// If the session is already registered, it returns nil (idempotent).
// The caller should use the returned session from GetSession for consistency.
func (r *Runtime) RegisterSession(sess *Session) error {
	if sess == nil {
		return fmt.Errorf("session is required")
	}

	sessionID := sess.Meta().ID

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return fmt.Errorf("session runtime closed")
	}
	if !r.started {
		return fmt.Errorf("session runtime not started")
	}

	// Check if worker already exists
	if worker, ok := r.workers[sessionID]; ok {
		// Ensure the session instance is the canonical one
		if worker.session != sess {
			// Same session ID but different instance - close the new one
			_ = sess.Close()
		}
		return nil
	}

	// Create new worker with callback to publish context tokens
	worker := newSessionWorker(sess, func(s *Session) { r.publishContextTokens(s) })
	r.workers[sessionID] = worker

	return nil
}

// getWorker retrieves an existing worker by session ID (internal use only).
func (r *Runtime) getWorker(sessionID string) (*sessionWorker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.closed {
		return nil, fmt.Errorf("session runtime closed")
	}
	if !r.started {
		return nil, fmt.Errorf("session runtime not started")
	}

	worker, ok := r.workers[sessionID]
	if !ok {
		return nil, fmt.Errorf("no worker found for session %s", sessionID)
	}
	return worker, nil
}

// GetSession retrieves the session for a given session ID.
func (r *Runtime) GetSession(sessionID string) (*Session, error) {
	worker, err := r.getWorker(sessionID)
	if err != nil {
		return nil, err
	}
	return worker.session, nil
}

// ListActiveSessions returns all currently active session IDs.
func (r *Runtime) ListActiveSessions() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sessionIDs := make([]string, 0, len(r.workers))
	for id := range r.workers {
		sessionIDs = append(sessionIDs, id)
	}
	return sessionIDs
}

// HandleCommandForSession processes a command for a specific session.
// This is the preferred API for router-targeted command dispatch.
func (r *Runtime) HandleCommandForSession(sessionID string, requestID string, cmdType event.EventType, payload any) error {
	worker, err := r.getWorker(sessionID)
	if err != nil {
		return err
	}

	switch cmdType {
	case event.UserMessage:
		p, ok := payload.(event.UserMessagePayload)
		if !ok {
			return fmt.Errorf("invalid payload for user_message")
		}
		return worker.handleUserMessage(r.runtimeCtx, requestID, p.Content)
	case event.NewConversation:
		if err := worker.session.ResetConversation(r.runtimeCtx); err != nil {
			return err
		}
		r.publishContextTokens(worker.session)

		// Publish event to notify client that conversation has been reset
		event.Publish(event.Event{
			Type:      event.NewConversation,
			SessionID: sessionID,
			RequestID: requestID,
		})
		return nil
	case event.ChangeProviderModel:
		p, ok := payload.(event.ChangeProviderModelPayload)
		if !ok {
			return fmt.Errorf("invalid payload for change_provider_model")
		}
		if err := worker.session.SetModel(r.runtimeCtx, p.Model); err != nil {
			return err
		}
		meta := worker.session.Meta()
		event.Publish(event.Event{
			Type: event.ChangeModelEvent,
			Payload: event.ChangeProviderModelPayload{
				Provider: meta.Provider,
				Model:    meta.Model,
			},
			SessionID: sessionID,
			RequestID: requestID,
		})
		return nil
	case event.CancelAgentRun:
		// Cancel is scoped to the originating session only.
		// A cancel from session A cannot interrupt session B.
		worker.cancelRun()
		// Emit cancelled event with session context for proper routing
		event.Publish(event.Event{
			Type:      event.AgentCancelled,
			Payload:   event.AgentCancelledPayload{},
			SessionID: sessionID,
			RequestID: requestID,
		})
		return nil
	case event.RequestProviderModels:
		p, ok := payload.(event.RequestProviderModelsPayload)
		if !ok {
			return fmt.Errorf("invalid payload for request_provider_models")
		}
		r.handleRequestProviderModels(p.Provider, sessionID, requestID)
		return nil
	default:
		return fmt.Errorf("unknown command type: %s", cmdType)
	}
}

// HandleCreateSessionRequest creates a session for the given provider/model.
// When force is false and provider matches the existing session, it may reuse
// the session and only switch model; when force is true, it always creates a new session.
// The clientType and clientIdentity are required for non-temporary sessions.
func (r *Runtime) HandleCreateSessionRequest(sessionID, provider, model, clientType, clientIdentity string, force bool) (string, *Session, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", nil, fmt.Errorf("session runtime closed")
	}
	if !r.started {
		r.mu.Unlock()
		return "", nil, fmt.Errorf("session runtime not started")
	}

	// Get the existing worker/session if available
	var existingWorker *sessionWorker
	if sessionID != "" {
		existingWorker = r.workers[sessionID]
	}
	r.mu.Unlock()

	provider, model, err := config.Must().ResolveProviderModel(provider, model)
	if err != nil {
		return "", nil, err
	}

	// If we have an existing session with the same provider, optionally reuse it.
	if !force && existingWorker != nil && existingWorker.session.Meta().Provider == provider {
		if err := existingWorker.session.SetModel(r.runtimeCtx, model); err != nil {
			return "", nil, err
		}
		return existingWorker.session.Meta().ID, existingWorker.session, nil
	}

	// Deactivate any existing active session for this client before creating new one
	if err := r.manager.DeactivateClientSessions(r.ctx, clientType, clientIdentity); err != nil {
		logger.Warning("failed to deactivate existing sessions",
			logger.BuildMetadata("client_type", clientType, "client_identity", clientIdentity, "error", err))
	}

	// Create a new session with client binding
	newSession, err := r.manager.Create(r.ctx, CreateSessionOptions{
		Provider:       provider,
		Model:          model,
		ClientType:     clientType,
		ClientIdentity: clientIdentity,
		Temporary:      false,
	})
	if err != nil {
		return "", nil, fmt.Errorf("create session for provider '%s' model '%s': %w", provider, model, err)
	}

	// Register the new session with the runtime
	if err := r.RegisterSession(newSession); err != nil {
		_ = newSession.Close()
		return "", nil, fmt.Errorf("register new session: %w", err)
	}

	logger.Info("session created via runtime",
		logger.BuildMetadata("session_id", newSession.Meta().ID, "provider", provider, "model", model,
			"client_type", clientType, "client_identity", clientIdentity))
	return newSession.Meta().ID, newSession, nil
}

// UnregisterSession removes a session worker from the runtime and releases its resources.
func (r *Runtime) UnregisterSession(sessionID string) error {
	r.mu.Lock()
	worker, ok := r.workers[sessionID]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	delete(r.workers, sessionID)
	r.mu.Unlock()

	worker.close()
	if err := worker.session.Close(); err != nil {
		return fmt.Errorf("close session %s: %w", sessionID, err)
	}

	logger.Debug("session unregistered", logger.BuildMetadata("session_id", sessionID))
	return nil
}

// CancelSessionRun cancels any in-progress run for a specific session.
func (r *Runtime) CancelSessionRun(sessionID string) error {
	worker, err := r.getWorker(sessionID)
	if err != nil {
		return err
	}
	worker.cancelRun()
	return nil
}

// Close stops all workers and releases runtime resources.
func (r *Runtime) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	if r.runtimeCancel != nil {
		r.runtimeCancel()
		r.runtimeCancel = nil
	}

	// Collect all workers to close
	workers := make([]*sessionWorker, 0, len(r.workers))
	sessions := make([]*Session, 0, len(r.workers))
	for _, w := range r.workers {
		workers = append(workers, w)
		sessions = append(sessions, w.session)
	}
	// Clear the map
	r.workers = make(map[string]*sessionWorker)
	r.mu.Unlock()

	// Close all workers and their sessions
	for _, w := range workers {
		w.close()
	}
	for _, s := range sessions {
		_ = s.Close()
	}

	r.wg.Wait()
}

// PublishContextTokens publishes context token information for the given session.
func (r *Runtime) PublishContextTokens(sessionID string) {
	worker, err := r.getWorker(sessionID)
	if err != nil {
		return
	}
	r.publishContextTokens(worker.session)
}

func (r *Runtime) publishContextTokens(sess *Session) {
	if sess == nil {
		return
	}
	promptTokens, estimated := sess.ContextTokenCount()
	event.Publish(event.Event{
		Type: event.ContextTokens,
		Payload: event.ContextTokensPayload{
			PromptTokens: promptTokens,
			Estimated:    estimated,
		},
		SessionID: sess.Meta().ID,
	})
}

func (r *Runtime) handleRequestProviderModels(provider string, sessionID string, requestID string) {
	r.mu.RLock()
	if !r.started || r.closed {
		r.mu.RUnlock()
		return
	}
	runtimeCtx := r.runtimeCtx
	manager := r.manager
	r.mu.RUnlock()

	r.wg.Go(func() {
		ctx, cancel := context.WithTimeout(runtimeCtx, 30*time.Second)
		defer cancel()

		models, err := manager.ListProviderModels(ctx, provider)
		payload := event.ProviderModelsResultPayload{
			Provider: provider,
			Models:   make([]event.ProviderModel, 0, len(models)),
		}
		if err != nil {
			payload.Error = err.Error()
		} else {
			for _, model := range models {
				payload.Models = append(payload.Models, event.ProviderModel{
					ID:          model.ID,
					DisplayName: model.DisplayName,
				})
			}
		}
		event.Publish(event.Event{
			Type:      event.ProviderModelsResult,
			Payload:   payload,
			SessionID: sessionID,
			RequestID: requestID,
		})
	})
}
