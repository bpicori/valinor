package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/tokens"
)

// Runtime owns the active session lifecycle and handles provider/model switch commands.
type Runtime struct {
	ctx         context.Context
	manager     *Manager
	sessionOpts SessionOptions
	bus         *bus.Bus

	mu            sync.Mutex
	active        *Session
	activeStop    context.CancelFunc
	runCancel     context.CancelFunc
	runInProgress bool
	subCommand    *bus.Subscription
	wg            sync.WaitGroup

	runtimeCtx    context.Context
	runtimeCancel context.CancelFunc
	started       bool
	closed        bool

	onActiveSessionChanged func(string)
}

// NewRuntime creates a session runtime with an initial active session.
func NewRuntime(
	ctx context.Context,
	manager *Manager,
	sessionOpts SessionOptions,
	initial *Session,
	eventBus *bus.Bus,
	onActiveSessionChanged func(string),
) *Runtime {
	return &Runtime{
		ctx:                    ctx,
		manager:                manager,
		sessionOpts:            sessionOpts,
		bus:                    eventBus,
		active:                 initial,
		onActiveSessionChanged: onActiveSessionChanged,
	}
}

// Start runs the initial session and starts listening for session command events.
func (r *Runtime) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || r.started {
		return
	}
	r.started = true
	r.runtimeCtx, r.runtimeCancel = context.WithCancel(r.ctx)

	r.subCommand = r.bus.Subscribe(
		bus.EventUserMessage,
		bus.EventNewConversation,
		bus.EventChangeProviderModel,
		bus.EventCreateNewSession,
		bus.EventRequestProviderModels,
		bus.EventCancelAgentRun,
	)
	r.startActiveSessionLocked()
	r.publishActiveContextTokens()
	r.startCommandHandlerLocked()
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
		case ev, ok := <-r.subCommand.Ch():
			if !ok {
				return
			}
			r.handleCommandEvent(ev)
		}
	}
}

func (r *Runtime) handleCommandEvent(ev bus.Event) {
	switch ev.Type {
	case bus.EventUserMessage:
		payload, ok := ev.Payload.(bus.UserMessagePayload)
		if !ok {
			return
		}
		if err := r.handleUserMessage(payload.Content); err != nil {
			applog.Error("session runtime user message command failed", applog.BuildMetadata("error", err))
		}
	case bus.EventNewConversation:
		r.handleNewConversation()
	case bus.EventChangeProviderModel:
		payload, ok := ev.Payload.(bus.ChangeProviderModelPayload)
		if !ok {
			return
		}
		if err := r.handleChangeProviderModel(payload.Provider, payload.Model); err != nil {
			applog.Error("session runtime change provider/model command failed", applog.BuildMetadata("provider", payload.Provider, "model", payload.Model, "error", err))
		}
	case bus.EventCreateNewSession:
		payload, ok := ev.Payload.(bus.CreateNewSessionPayload)
		if !ok {
			return
		}
		if _, err := r.HandleCreateSessionRequest(payload.Provider, payload.Model); err != nil {
			applog.Error("session runtime create-session command failed", applog.BuildMetadata("provider", payload.Provider, "model", payload.Model, "error", err))
		}
	case bus.EventCancelAgentRun:
		r.cancelCurrentRun()
	case bus.EventRequestProviderModels:
		payload, ok := ev.Payload.(bus.RequestProviderModelsPayload)
		if !ok {
			return
		}
		r.handleRequestProviderModels(payload.Provider)
	}
}

func (r *Runtime) handleUserMessage(content string) error {
	r.mu.Lock()
	if !r.started || r.closed {
		r.mu.Unlock()
		return fmt.Errorf("session runtime unavailable")
	}
	if r.active == nil {
		r.mu.Unlock()
		return fmt.Errorf("no active session")
	}
	active := r.active
	runtimeCtx := r.runtimeCtx

	// Cancel any existing run before starting a new one
	if r.runCancel != nil {
		r.runCancel()
	}

	runCtx, runCancel := context.WithCancel(runtimeCtx)
	r.runCancel = runCancel
	r.runInProgress = true
	r.mu.Unlock()

	r.wg.Go(func() {
		defer func() {
			r.mu.Lock()
			r.runCancel = nil
			r.runInProgress = false
			r.mu.Unlock()
		}()

		if err := active.HandleUserMessage(runCtx, content); err != nil {
			if runCtx.Err() == context.Canceled {
				applog.Info("session runtime user message cancelled", nil)
				r.bus.Publish(bus.Event{
					Type:    bus.EventAgentCancelled,
					Payload: bus.AgentCancelledPayload{},
				})
				return
			}
			applog.Error("session runtime user message command failed", applog.BuildMetadata("error", err))
		}
	})

	return nil
}

func (r *Runtime) handleNewConversation() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.started || r.closed || r.active == nil {
		return
	}
	r.active.ResetConversation()
	r.publishActiveContextTokens()
}

func (r *Runtime) handleChangeProviderModel(provider, model string) error {
	r.mu.Lock()
	if !r.started || r.closed {
		r.mu.Unlock()
		return fmt.Errorf("session runtime unavailable")
	}
	if r.active == nil {
		r.mu.Unlock()
		return fmt.Errorf("no active session")
	}
	active := r.active
	runtimeCtx := r.runtimeCtx
	r.mu.Unlock()

	return active.SwitchProviderModel(runtimeCtx, provider, model)
}

func (r *Runtime) cancelCurrentRun() {
	r.mu.Lock()
	if r.runCancel != nil {
		r.runCancel()
		r.runCancel = nil
	}
	r.mu.Unlock()
}

func (r *Runtime) handleRequestProviderModels(provider string) {
	r.mu.Lock()
	if !r.started || r.closed {
		r.mu.Unlock()
		return
	}
	runtimeCtx := r.runtimeCtx
	manager := r.manager
	r.mu.Unlock()

	r.wg.Go(func() {
		ctx, cancel := context.WithTimeout(runtimeCtx, 30*time.Second)
		defer cancel()

		models, err := manager.ListProviderModels(ctx, provider)
		payload := bus.ProviderModelsResultPayload{
			Provider: provider,
			Models:   make([]bus.ProviderModel, 0, len(models)),
		}
		if err != nil {
			payload.Error = err.Error()
		} else {
			for _, model := range models {
				payload.Models = append(payload.Models, bus.ProviderModel{
					ID:          model.ID,
					DisplayName: model.DisplayName,
				})
			}
		}
		r.bus.Publish(bus.Event{
			Type:    bus.EventProviderModelsResult,
			Payload: payload,
		})
	})
}

// HandleCreateSessionRequest handles provider/model changes initiated by UI or web handlers.
// If provider is unchanged, it updates model in the current session.
// If provider changes, it creates and switches to a new session.
func (r *Runtime) HandleCreateSessionRequest(provider, model string) (string, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", fmt.Errorf("session runtime closed")
	}
	if !r.started {
		r.mu.Unlock()
		return "", fmt.Errorf("session runtime not started")
	}
	if r.active == nil {
		r.mu.Unlock()
		return "", fmt.Errorf("no active session")
	}
	active := r.active
	runtimeCtx := r.runtimeCtx
	opts := r.sessionOpts
	r.mu.Unlock()

	provider, model, err := r.manager.config.ResolveProviderModel(provider, model)
	if err != nil {
		return "", err
	}

	if active.Meta().Provider == provider {
		if err := active.SwitchProviderModel(runtimeCtx, provider, model); err != nil {
			return "", err
		}
		return active.Meta().ID, nil
	}

	newOpts := opts
	newOpts.Provider = provider
	newOpts.Model = model
	newSession, err := r.manager.Create(r.ctx, newOpts)
	if err != nil {
		return "", fmt.Errorf("create session for provider '%s' model '%s': %w", provider, model, err)
	}

	r.mu.Lock()
	if r.closed || !r.started {
		r.mu.Unlock()
		_ = newSession.Close()
		return "", fmt.Errorf("session runtime unavailable")
	}
	oldSession := r.detachActiveLocked()
	r.active = newSession
	r.startActiveSessionLocked()
	r.publishActiveContextTokens()
	activeSessionID := r.active.Meta().ID
	onActiveSessionChanged := r.onActiveSessionChanged
	r.mu.Unlock()

	if oldSession != nil {
		_ = oldSession.Close()
	}
	if onActiveSessionChanged != nil {
		onActiveSessionChanged(activeSessionID)
	}
	applog.Info("session switched", applog.BuildMetadata("session_id", activeSessionID, "provider", provider, "model", model))
	return activeSessionID, nil
}

// Close stops the active session and releases runtime resources.
func (r *Runtime) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	if r.subCommand != nil {
		r.bus.Unsubscribe(r.subCommand)
		r.subCommand = nil
	}
	// Cancel any in-flight run before shutting down
	if r.runCancel != nil {
		r.runCancel()
		r.runCancel = nil
	}
	if r.runtimeCancel != nil {
		r.runtimeCancel()
		r.runtimeCancel = nil
	}
	activeSession := r.detachActiveLocked()
	r.mu.Unlock()
	if activeSession != nil {
		_ = activeSession.Close()
	}
	r.wg.Wait()
}

// PublishActiveContextTokens publishes context token information for the active session.
func (r *Runtime) PublishActiveContextTokens() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.closed {
		return
	}
	r.publishActiveContextTokens()
}

func (r *Runtime) startActiveSessionLocked() {
	if r.active == nil {
		return
	}
	activeCtx, activeStop := context.WithCancel(r.runtimeCtx)
	r.activeStop = activeStop
	r.wg.Go(func() {
		<-activeCtx.Done()
	})
}

func (r *Runtime) detachActiveLocked() *Session {
	if r.activeStop != nil {
		r.activeStop()
		r.activeStop = nil
	}
	active := r.active
	r.active = nil
	return active
}

func (r *Runtime) publishActiveContextTokens() {
	if r.active == nil {
		return
	}
	messages := r.active.agentContext.Messages()
	promptTokens := tokens.EstimatePromptTokens(messages)
	r.bus.Publish(bus.Event{
		Type: bus.EventContextTokens,
		Payload: bus.ContextTokensPayload{
			PromptTokens: promptTokens,
			Estimated:    true,
		},
	})
}
