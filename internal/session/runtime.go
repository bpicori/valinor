package session

import (
	"context"
	"fmt"
	"sync"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/tokens"
)

// Runtime owns the active session lifecycle and handles provider/model switch commands.
type Runtime struct {
	ctx          context.Context
	manager      *Manager
	createConfig CreateOptions
	bus          *bus.Bus

	mu         sync.Mutex
	active     *Session
	activeStop context.CancelFunc
	subCommand *bus.Subscription
	wg         sync.WaitGroup

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
	createConfig CreateOptions,
	initial *Session,
	eventBus *bus.Bus,
	onActiveSessionChanged func(string),
) *Runtime {
	return &Runtime{
		ctx:                    ctx,
		manager:                manager,
		createConfig:           createConfig,
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
			applog.Error("session runtime user message command failed", applog.M("error", err))
		}
	case bus.EventNewConversation:
		r.handleNewConversation()
	case bus.EventChangeProviderModel:
		payload, ok := ev.Payload.(bus.ChangeProviderModelPayload)
		if !ok {
			return
		}
		if err := r.handleChangeProviderModel(payload.Provider, payload.Model); err != nil {
			applog.Error("session runtime change provider/model command failed", applog.M("provider", payload.Provider, "model", payload.Model, "error", err))
		}
	case bus.EventCreateNewSession:
		payload, ok := ev.Payload.(bus.CreateNewSessionPayload)
		if !ok {
			return
		}
		if _, err := r.HandleCreateSessionRequest(payload.Provider, payload.Model); err != nil {
			applog.Error("session runtime create-session command failed", applog.M("provider", payload.Provider, "model", payload.Model, "error", err))
		}
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
	r.mu.Unlock()

	return active.HandleUserMessage(runtimeCtx, content)
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
	createCfg := r.createConfig
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

	newConfig := createCfg
	newConfig.Provider = provider
	newConfig.Model = model
	newSession, err := r.manager.Create(r.ctx, newConfig)
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
	applog.Info("session switched", applog.M("session_id", activeSessionID, "provider", provider, "model", model))
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
