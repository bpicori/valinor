package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/event"
	"valinor/internal/tokens"
)

// EventEmitter is a callback for emitting events with session context.
// The router provides this to route events to the correct consumers.
type EventEmitter func(sessionID string, ev event.Event)

// sessionWorker owns the execution state for a single session.
// Each worker processes commands sequentially for its session only.
type sessionWorker struct {
	session *Session

	mu            sync.Mutex
	runCancel     context.CancelFunc
	runInProgress bool
	closed        bool
	runGeneration uint64 // Incremented on each new run to prevent stale clears
}

// newSessionWorker creates a new worker for the given session.
func newSessionWorker(sess *Session) *sessionWorker {
	return &sessionWorker{
		session: sess,
	}
}

// handleUserMessage processes a user message for this session.
// It manages its own run cancellation state independently.
// Uses run generation counter to prevent older runs from clearing newer run state.
func (w *sessionWorker) handleUserMessage(ctx context.Context, requestID string, content string, bus *bus.Bus) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return fmt.Errorf("session worker closed")
	}

	// Cancel any existing run before starting a new one
	if w.runCancel != nil {
		w.runCancel()
	}

	// Increment generation for this new run
	w.runGeneration++
	myGeneration := w.runGeneration

	runCtx, runCancel := context.WithCancel(ctx)
	w.runCancel = runCancel
	w.runInProgress = true
	w.mu.Unlock()

	// Execute the run
	err := w.session.HandleUserMessage(runCtx, requestID, content)

	// Clear run state only if this is still the current generation
	// This prevents older (cancelled) runs from clearing a newer run's state
	w.mu.Lock()
	if w.runGeneration == myGeneration {
		w.runCancel = nil
		w.runInProgress = false
	}
	w.mu.Unlock()

	if err != nil {
		if runCtx.Err() == context.Canceled {
			applog.Info("session worker user message cancelled", applog.BuildMetadata("session_id", w.session.Meta().ID))
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

// close marks the worker as closed and cancels any in-progress run.
func (w *sessionWorker) close() {
	w.mu.Lock()
	w.closed = true
	if w.runCancel != nil {
		w.runCancel()
		w.runCancel = nil
	}
	w.mu.Unlock()
}

// Runtime owns the session worker registry and handles command dispatch.
type Runtime struct {
	ctx         context.Context
	manager     *Manager
	sessionOpts SessionOptions
	bus         *bus.Bus

	mu          sync.RWMutex
	workers     map[string]*sessionWorker // sessionID -> worker
	runtimeCtx  context.Context
	runtimeCancel context.CancelFunc
	started     bool
	closed      bool
	wg          sync.WaitGroup

	busSub *bus.Subscription
}

// NewRuntime creates a session runtime with a worker registry.
func NewRuntime(
	ctx context.Context,
	manager *Manager,
	sessionOpts SessionOptions,
	eventBus *bus.Bus,
) *Runtime {
	return &Runtime{
		ctx:         ctx,
		manager:     manager,
		sessionOpts: sessionOpts,
		bus:         eventBus,
		workers:     make(map[string]*sessionWorker),
	}
}

// Start starts the runtime's command handling loop.
func (r *Runtime) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || r.started {
		return
	}
	r.started = true
	r.runtimeCtx, r.runtimeCancel = context.WithCancel(r.ctx)

	// Subscribe to all command events from the bus
	r.busSub = r.bus.Subscribe(
		event.UserMessage,
		event.NewConversation,
		event.ChangeProviderModel,
		event.CreateNewSession,
		event.RequestProviderModels,
		event.CancelAgentRun,
	)

	r.startCommandHandlerLocked()
}

// GetOrCreateWorker gets an existing worker for the session or creates a new one.
// Returns the worker and a bool indicating if it was created (true) or existing (false).
func (r *Runtime) GetOrCreateWorker(sess *Session) (*sessionWorker, bool, error) {
	if sess == nil {
		return nil, false, fmt.Errorf("session is required")
	}

	sessionID := sess.Meta().ID

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, false, fmt.Errorf("session runtime closed")
	}
	if !r.started {
		return nil, false, fmt.Errorf("session runtime not started")
	}

	// Check if worker already exists
	if worker, ok := r.workers[sessionID]; ok {
		// Ensure the session instance is the canonical one
		if worker.session != sess {
			// Same session ID but different instance - close the new one
			_ = sess.Close()
		}
		return worker, false, nil
	}

	// Create new worker
	worker := newSessionWorker(sess)
	r.workers[sessionID] = worker

	return worker, true, nil
}

// GetWorker retrieves an existing worker by session ID.
func (r *Runtime) GetWorker(sessionID string) (*sessionWorker, error) {
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
	worker, err := r.GetWorker(sessionID)
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

func (r *Runtime) startCommandHandlerLocked() {
	r.wg.Add(1)
	go r.runCommandHandlerLoop()
}

func (r *Runtime) runCommandHandlerLoop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.runtimeCtx.Done():
			return
		case ev, ok := <-r.busSub.Ch():
			if !ok {
				return
			}
			r.handleCommandEvent(ev)
		}
	}
}

// handleCommandEvent processes a command event from the bus.
// Events without explicit session context are currently broadcast to all workers
// (legacy behavior). Per-session routing requires session context in the event.
func (r *Runtime) handleCommandEvent(ev event.Event) {
	switch ev.Type {
	case event.UserMessage:
		payload, ok := ev.Payload.(event.UserMessagePayload)
		if !ok {
			return
		}
		// Session-scoped delivery: if event has session context, deliver only to that session's worker.
		// Only broadcast to all workers if no session context is available (legacy fallback).
		if ev.SessionID != "" {
			if worker, err := r.GetWorker(ev.SessionID); err == nil {
				if err := worker.handleUserMessage(r.runtimeCtx, ev.RequestID, payload.Content, r.bus); err != nil {
					applog.Error("session worker user message failed",
						applog.BuildMetadata("session_id", worker.session.Meta().ID, "error", err))
				}
			}
		} else {
			// Legacy fallback: broadcast to all workers (for events without session context)
			r.dispatchToAllWorkers(func(w *sessionWorker) {
				if err := w.handleUserMessage(r.runtimeCtx, ev.RequestID, payload.Content, r.bus); err != nil {
					applog.Error("session worker user message failed",
						applog.BuildMetadata("session_id", w.session.Meta().ID, "error", err))
				}
			})
		}
	case event.NewConversation:
		// Broadcast to all workers
		r.dispatchToAllWorkers(func(w *sessionWorker) {
			w.session.ResetConversation()
			r.publishContextTokens(w.session)
		})
	case event.ChangeProviderModel:
		payload, ok := ev.Payload.(event.ChangeProviderModelPayload)
		if !ok {
			return
		}
		// Broadcast to all workers
		r.dispatchToAllWorkers(func(w *sessionWorker) {
			if err := w.session.SwitchProviderModel(r.runtimeCtx, payload.Provider, payload.Model); err != nil {
				applog.Error("session worker change provider/model failed",
					applog.BuildMetadata("session_id", w.session.Meta().ID, "provider", payload.Provider, "model", payload.Model, "error", err))
			}
		})
	case event.CreateNewSession:
		// This is handled by HandleCreateSessionRequest which has explicit session context
		applog.Debug("runtime: create_new_session should be handled via HandleCreateSessionRequest", nil)
	case event.CancelAgentRun:
		// Session-scoped cancel: if event has session context, cancel only that session's worker.
		// Only broadcast to all workers if no session context is available (legacy fallback).
		if ev.SessionID != "" {
			if worker, err := r.GetWorker(ev.SessionID); err == nil {
				worker.cancelRun()
				// Emit cancelled event with session context for proper routing
				r.bus.Publish(event.Event{
					Type:      event.AgentCancelled,
					Payload:   event.AgentCancelledPayload{},
					SessionID: ev.SessionID,
					RequestID: ev.RequestID,
				})
			}
		} else {
			// Legacy fallback: broadcast cancel to all workers (for events without session context)
			r.dispatchToAllWorkers(func(w *sessionWorker) {
				w.cancelRun()
			})
		}
	case event.RequestProviderModels:
		payload, ok := ev.Payload.(event.RequestProviderModelsPayload)
		if !ok {
			return
		}
		// For bus events without session context, broadcast the result to all sessions
		// The router will filter based on session binding
		r.handleRequestProviderModels(payload.Provider, ev.SessionID, ev.RequestID)
	}
}

// dispatchToAllWorkers executes the given function for all workers.
func (r *Runtime) dispatchToAllWorkers(fn func(*sessionWorker)) {
	r.mu.RLock()
	workers := make([]*sessionWorker, 0, len(r.workers))
	for _, w := range r.workers {
		workers = append(workers, w)
	}
	r.mu.RUnlock()

	for _, w := range workers {
		fn(w)
	}
}

// HandleCommandForSession processes a command for a specific session.
// This is the preferred API for router-targeted command dispatch.
func (r *Runtime) HandleCommandForSession(sessionID string, requestID string, cmdType event.EventType, payload any) error {
	worker, err := r.GetWorker(sessionID)
	if err != nil {
		return err
	}

	switch cmdType {
	case event.UserMessage:
		p, ok := payload.(event.UserMessagePayload)
		if !ok {
			return fmt.Errorf("invalid payload for user_message")
		}
		return worker.handleUserMessage(r.runtimeCtx, requestID, p.Content, r.bus)
	case event.NewConversation:
		worker.session.ResetConversation()
		r.publishContextTokens(worker.session)
		return nil
	case event.ChangeProviderModel:
		p, ok := payload.(event.ChangeProviderModelPayload)
		if !ok {
			return fmt.Errorf("invalid payload for change_provider_model")
		}
		return worker.session.SwitchProviderModel(r.runtimeCtx, p.Provider, p.Model)
	case event.CancelAgentRun:
		// Cancel is scoped to the originating session only.
		// A cancel from session A cannot interrupt session B.
		worker.cancelRun()
		// Emit cancelled event with session context for proper routing
		r.bus.Publish(event.Event{
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

// HandleCreateSessionRequest creates a new session for the given provider/model.
// If the provider is unchanged from an existing session, updates the model instead.
func (r *Runtime) HandleCreateSessionRequest(sessionID, provider, model string) (string, *Session, error) {
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

	provider, model, err := r.manager.config.ResolveProviderModel(provider, model)
	if err != nil {
		return "", nil, err
	}

	// If we have an existing session with the same provider, just switch the model
	if existingWorker != nil && existingWorker.session.Meta().Provider == provider {
		if err := existingWorker.session.SwitchProviderModel(r.runtimeCtx, provider, model); err != nil {
			return "", nil, err
		}
		return existingWorker.session.Meta().ID, existingWorker.session, nil
	}

	// Create a new session
	newOpts := r.sessionOpts
	newOpts.Provider = provider
	newOpts.Model = model
	newSession, err := r.manager.Create(r.ctx, newOpts)
	if err != nil {
		return "", nil, fmt.Errorf("create session for provider '%s' model '%s': %w", provider, model, err)
	}

	// Create worker for the new session
	worker, _, err := r.GetOrCreateWorker(newSession)
	if err != nil {
		_ = newSession.Close()
		return "", nil, fmt.Errorf("create worker for new session: %w", err)
	}

	applog.Info("session created via runtime",
		applog.BuildMetadata("session_id", newSession.Meta().ID, "provider", provider, "model", model))
	return newSession.Meta().ID, worker.session, nil
}

// CancelSessionRun cancels any in-progress run for a specific session.
func (r *Runtime) CancelSessionRun(sessionID string) error {
	worker, err := r.GetWorker(sessionID)
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
	if r.busSub != nil {
		r.bus.Unsubscribe(r.busSub)
		r.busSub = nil
	}
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
	worker, err := r.GetWorker(sessionID)
	if err != nil {
		return
	}
	r.publishContextTokens(worker.session)
}

func (r *Runtime) publishContextTokens(sess *Session) {
	if sess == nil {
		return
	}
	messages := sess.agentContext.Messages()
	promptTokens := tokens.EstimatePromptTokens(messages)
	r.bus.Publish(event.Event{
		Type:      event.ContextTokens,
		Payload:   event.ContextTokensPayload{
			PromptTokens: promptTokens,
			Estimated:    true,
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
		r.bus.Publish(event.Event{
			Type:      event.ProviderModelsResult,
			Payload:   payload,
			SessionID: sessionID,
			RequestID: requestID,
		})
	})
}

// EnsureActiveSession is deprecated. Use GetOrCreateWorker instead.
// This method is kept for backward compatibility during the transition.
func (r *Runtime) EnsureActiveSession(sess *Session) (*Session, error) {
	worker, _, err := r.GetOrCreateWorker(sess)
	if err != nil {
		return nil, err
	}
	return worker.session, nil
}

// ActiveSession is deprecated. Use GetSession instead.
// This method is kept for backward compatibility and returns the first available session.
func (r *Runtime) ActiveSession() (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.closed {
		return nil, fmt.Errorf("session runtime closed")
	}
	if !r.started {
		return nil, fmt.Errorf("session runtime not started")
	}

	// Return the first available session (for backward compatibility)
	for _, w := range r.workers {
		return w.session, nil
	}
	return nil, fmt.Errorf("no active session")
}
