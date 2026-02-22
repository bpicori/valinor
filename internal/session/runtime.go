package session

import (
	"context"
	"fmt"
	"sync"

	"valinor/internal/applog"
	"valinor/internal/bus"
)

// Runtime owns the active session lifecycle and handles provider/model switch commands.
type Runtime struct {
	ctx          context.Context
	manager      *Manager
	createConfig CreateConfig
	bus          *bus.Bus

	mu         sync.Mutex
	active     *Session
	activeStop context.CancelFunc
	subCreate  *bus.Subscription
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
	createConfig CreateConfig,
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

// Start runs the initial session and starts listening for create-session commands.
func (r *Runtime) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || r.started {
		return
	}
	r.started = true
	r.runtimeCtx, r.runtimeCancel = context.WithCancel(r.ctx)

	r.subCreate = r.bus.Subscribe(bus.EventCreateNewSession)
	r.startActiveSessionLocked()
	r.startCreateSessionHandlerLocked()
}

func (r *Runtime) startCreateSessionHandlerLocked() {
	r.wg.Add(1)
	go r.runCreateSessionHandlerLoop()
}

func (r *Runtime) runCreateSessionHandlerLoop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.runtimeCtx.Done():
			return
		case ev, ok := <-r.subCreate.Ch():
			if !ok {
				return
			}
			if ev.Type != bus.EventCreateNewSession {
				continue
			}
			payload, ok := ev.Payload.(bus.CreateNewSessionPayload)
			if !ok {
				continue
			}
			if _, err := r.HandleCreateSessionRequest(payload.Provider, payload.Model); err != nil {
				applog.Error("session runtime create-session command failed", applog.M("provider", payload.Provider, "model", payload.Model, "error", err))
			}
		}
	}
}

// HandleCreateSessionRequest handles provider/model changes initiated by UI or web handlers.
// If provider is unchanged, it updates model in the current session.
// If provider changes, it creates and switches to a new session.
func (r *Runtime) HandleCreateSessionRequest(provider, model string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return "", fmt.Errorf("session runtime closed")
	}
	if !r.started {
		return "", fmt.Errorf("session runtime not started")
	}
	if r.active == nil {
		return "", fmt.Errorf("no active session")
	}

	if provider == "" {
		provider = r.createConfig.Config.DefaultProvider
	}
	providerCfg, ok := r.createConfig.Config.Providers[provider]
	if !ok {
		return "", fmt.Errorf("provider '%s' not found in config", provider)
	}
	if model == "" {
		model = providerCfg.DefaultModel
	}
	if model == "" {
		return "", fmt.Errorf("provider and model are required")
	}

	if r.active.Meta().Provider == provider {
		r.bus.Publish(bus.Event{
			Type:    bus.EventChangeProviderModel,
			Payload: bus.ChangeProviderModelPayload{Provider: provider, Model: model},
		})
		return r.active.Meta().ID, nil
	}

	newConfig := r.createConfig
	newConfig.Provider = provider
	newConfig.Model = model
	newSession, err := r.manager.Create(r.ctx, newConfig)
	if err != nil {
		return "", fmt.Errorf("create session for provider '%s' model '%s': %w", provider, model, err)
	}

	r.stopActiveLocked()
	r.active = newSession
	r.startActiveSessionLocked()

	activeSessionID := r.active.Meta().ID
	if r.onActiveSessionChanged != nil {
		r.onActiveSessionChanged(activeSessionID)
	}
	r.bus.Publish(bus.Event{
		Type:    bus.EventChangeProviderModel,
		Payload: bus.ChangeProviderModelPayload{Provider: provider, Model: model},
	})
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
	if r.subCreate != nil {
		r.bus.Unsubscribe(r.subCreate)
		r.subCreate = nil
	}
	if r.runtimeCancel != nil {
		r.runtimeCancel()
		r.runtimeCancel = nil
	}
	r.stopActiveLocked()
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Runtime) startActiveSessionLocked() {
	if r.active == nil {
		return
	}
	activeCtx, activeStop := context.WithCancel(r.runtimeCtx)
	r.activeStop = activeStop
	active := r.active
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		active.Run(activeCtx)
	}()
}

func (r *Runtime) stopActiveLocked() {
	if r.activeStop != nil {
		r.activeStop()
		r.activeStop = nil
	}
	if r.active != nil {
		_ = r.active.Close()
		r.active = nil
	}
}
