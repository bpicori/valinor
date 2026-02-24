// Package router provides the internal API boundary for routing commands and events.
package router

import (
	"context"
	"fmt"
	"sync"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/session"
)

// ClientEventSink is the interface for sending events to a specific client.
// The gateway implements this to forward events to connected WebSocket clients.
type ClientEventSink interface {
	// SendEvent sends an event to the client. Non-blocking: drops event if client buffer full.
	SendEvent(event Event) bool
	// Close closes the client connection.
	Close()
}

// Router is the central routing component that manages client connections,
// session bindings, and routes commands/events between clients and the session core.
type Router interface {
	// HandleCommand processes a command from a client.
	HandleCommand(ctx context.Context, cmd Command) error

	// SubscribeClient registers a client event sink for an identity.
	// Returns the session binding for this identity (creates/gets session).
	SubscribeClient(ctx context.Context, identity ClientIdentity, sink ClientEventSink) (*SessionBinding, error)

	// UnsubscribeClient unregisters a client event sink.
	UnsubscribeClient(identity ClientIdentity)

	// Start starts the router event forwarding loop.
	Start(ctx context.Context)

	// Stop stops the router and cleans up.
	Stop()
}

// DefaultRouter implements the Router interface, bridging clients to the session core.
type DefaultRouter struct {
	bus         *bus.Bus
	runtime     *session.Runtime
	manager     *session.Manager
	sessionOpts session.SessionOptions
	identityMap *session.IdentityMapStore

	registry *SessionRegistry

	mu          sync.RWMutex
	clientSinks map[string]ClientEventSink // key: identity key

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	busSub *bus.Subscription
}

// NewRouter creates a new router instance.
func NewRouter(
	b *bus.Bus,
	runtime *session.Runtime,
	manager *session.Manager,
	sessionOpts session.SessionOptions,
	identityMap *session.IdentityMapStore,
) *DefaultRouter {
	return &DefaultRouter{
		bus:         b,
		runtime:     runtime,
		manager:     manager,
		sessionOpts: sessionOpts,
		identityMap: identityMap,
		registry:    NewSessionRegistry(),
		clientSinks: make(map[string]ClientEventSink),
	}
}

// Start starts the router's event forwarding loop.
func (r *DefaultRouter) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ctx != nil {
		return // already started
	}

	r.ctx, r.cancel = context.WithCancel(ctx)

	// Subscribe to all agent events from the bus
	r.busSub = r.bus.Subscribe(
		bus.EventAgentContentDelta,
		bus.EventAgentContentFull,
		bus.EventAgentThinking,
		bus.EventAgentStepTokens,
		bus.EventContextTokens,
		bus.EventAgentRefusal,
		bus.EventAgentToolCallStart,
		bus.EventAgentToolCallDone,
		bus.EventAgentDone,
		bus.EventAgentError,
		bus.EventAgentCancelled,
	)

	// Start the event forwarding goroutine
	r.wg.Add(1)
	go r.eventForwardingLoop()

	applog.Info("router started", nil)
}

// Stop stops the router and cleans up resources.
func (r *DefaultRouter) Stop() {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.busSub != nil {
		r.bus.Unsubscribe(r.busSub)
		r.busSub = nil
	}
	r.mu.Unlock()

	// Wait for goroutines to finish
	r.wg.Wait()

	// Close all client sinks
	r.mu.Lock()
	for key, sink := range r.clientSinks {
		sink.Close()
		delete(r.clientSinks, key)
	}
	r.mu.Unlock()

	applog.Info("router stopped", applog.M("bindings", r.registry.Count()))
}

// SubscribeClient registers a client event sink and returns/creates their session binding.
func (r *DefaultRouter) SubscribeClient(ctx context.Context, identity ClientIdentity, sink ClientEventSink) (*SessionBinding, error) {
	// Validate identity
	if err := ValidateClientIdentity(identity); err != nil {
		return nil, fmt.Errorf("invalid client identity: %w", err)
	}

	key := identity.Key()

	// Check if we already have an in-memory binding
	if binding := r.registry.GetByKey(key); binding != nil {
		// Register the new sink (replaces any existing sink for this identity)
		r.mu.Lock()
		if oldSink, ok := r.clientSinks[key]; ok {
			oldSink.Close()
		}
		r.clientSinks[key] = sink
		r.mu.Unlock()

		applog.Info("router: client reconnected to in-memory session",
			applog.M("identity_key", key, "session_id", binding.SessionID))

		// Send session attached event
		sink.SendEvent(Event{
			Type: EventSessionAttached,
			Payload: SessionAttachedPayload{
				SessionKey: binding.PublicKey,
				Provider:   binding.Session.Meta().Provider,
				Model:      binding.Session.Meta().Model,
			},
		})

		return binding, nil
	}

	// Try to get or create a session for this identity
	sess, isNew, err := r.getOrCreateSessionForIdentity(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("get or create session for identity %s: %w", key, err)
	}

	// Register the binding
	binding := r.registry.Register(identity, sess)

	// Update identity map with current session
	if r.identityMap != nil {
		if err := r.identityMap.Set(key, sess.Meta().ID, identity.Type); err != nil {
			applog.Warning("router: failed to save identity mapping",
				applog.M("error", err, "identity_key", key))
			// Continue - this is not fatal
		}
	}

	// Store the sink
	r.mu.Lock()
	r.clientSinks[key] = sink
	r.mu.Unlock()

	// Send session attached event
	sink.SendEvent(Event{
		Type: EventSessionAttached,
		Payload: SessionAttachedPayload{
			SessionKey: binding.PublicKey,
			Provider:   binding.Session.Meta().Provider,
			Model:      binding.Session.Meta().Model,
		},
	})

	if isNew {
		applog.Info("router: new client subscribed with new session",
			applog.M("identity_key", key, "session_id", binding.SessionID))
	} else {
		applog.Info("router: new client subscribed with resumed session",
			applog.M("identity_key", key, "session_id", binding.SessionID))
	}

	return binding, nil
}

// UnsubscribeClient unregisters a client event sink.
func (r *DefaultRouter) UnsubscribeClient(identity ClientIdentity) {
	key := identity.Key()

	r.mu.Lock()
	if sink, ok := r.clientSinks[key]; ok {
		sink.Close()
		delete(r.clientSinks, key)
	}
	r.mu.Unlock()

	applog.Debug("router: client unsubscribed",
		applog.M("identity_key", key))
}

// HandleCommand processes a command from a client.
func (r *DefaultRouter) HandleCommand(ctx context.Context, cmd Command) error {
	// Validate we have a valid identity
	if err := ValidateClientIdentity(cmd.Identity); err != nil {
		return fmt.Errorf("invalid command identity: %w", err)
	}

	key := cmd.Identity.Key()

	// Ensure this identity has an active session
	binding := r.registry.GetByKey(key)
	if binding == nil {
		return fmt.Errorf("no session found for identity %s", key)
	}

	applog.Debug("router: handling command",
		applog.M("identity_key", key,
			"command_type", cmd.Type,
			"session_id", binding.SessionID))

	// Convert router command to bus event and publish
	busEvent := r.commandToBusEvent(cmd)
	r.bus.Publish(busEvent)

	return nil
}

// commandToBusEvent converts a router command to a legacy bus event.
func (r *DefaultRouter) commandToBusEvent(cmd Command) bus.Event {
	switch cmd.Type {
	case CommandUserMessage:
		if p, ok := cmd.Payload.(UserMessagePayload); ok {
			return bus.Event{
				Type:    bus.EventUserMessage,
				Payload: bus.UserMessagePayload{Content: p.Content},
			}
		}
	case CommandNewConversation:
		return bus.Event{
			Type: bus.EventNewConversation,
		}
	case CommandChangeProviderModel:
		if p, ok := cmd.Payload.(ChangeProviderModelPayload); ok {
			return bus.Event{
				Type: bus.EventChangeProviderModel,
				Payload: bus.ChangeProviderModelPayload{
					Provider: p.Provider,
					Model:    p.Model,
				},
			}
		}
	case CommandCancelAgentRun:
		return bus.Event{
			Type: bus.EventCancelAgentRun,
		}
	}

	// Default empty event (should not reach here in practice)
	return bus.Event{}
}

// getOrCreateSessionForIdentity gets an existing session from the identity map or creates a new one.
// Returns the session, a boolean indicating if it's new, and any error.
func (r *DefaultRouter) getOrCreateSessionForIdentity(ctx context.Context, identity ClientIdentity) (*session.Session, bool, error) {
	key := identity.Key()

	// Check identity map for existing session
	if r.identityMap != nil {
		if mapping := r.identityMap.Get(key); mapping != nil {
			// Try to resume the existing session
			sess, err := r.manager.Resume(ctx, mapping.SessionID, r.sessionOpts)
			if err == nil {
				applog.Info("router: resumed session from identity map",
					applog.M("identity_key", key, "session_id", mapping.SessionID))
				return sess, false, nil
			}
			applog.Warning("router: failed to resume mapped session, creating new",
				applog.M("identity_key", key, "session_id", mapping.SessionID, "error", err))
			// Remove stale mapping
			_ = r.identityMap.Remove(key)
		}
	}

	// Create a new session
	sess, err := r.manager.Create(ctx, r.sessionOpts)
	if err != nil {
		return nil, false, fmt.Errorf("manager create session: %w", err)
	}

	return sess, true, nil
}

// eventForwardingLoop forwards bus events to connected clients.
func (r *DefaultRouter) eventForwardingLoop() {
	defer r.wg.Done()

	for {
		select {
		case <-r.ctx.Done():
			applog.Debug("router: event forwarding loop exiting", nil)
			return
		case ev, ok := <-r.busSub.Ch():
			if !ok {
				applog.Debug("router: event channel closed", nil)
				return
			}
			r.forwardEventToClients(ev)
		}
	}
}

// forwardEventToClients converts a bus event and sends to all connected clients.
func (r *DefaultRouter) forwardEventToClients(be bus.Event) {
	// Convert to router event
	routerEvent := FromBusEvent(be)
	if routerEvent.Type == "" {
		// Unknown event type, skip
		return
	}

	r.mu.RLock()
	sinks := make([]ClientEventSink, 0, len(r.clientSinks))
	for _, sink := range r.clientSinks {
		sinks = append(sinks, sink)
	}
	r.mu.RUnlock()

	// Send to all clients (non-blocking)
	for _, sink := range sinks {
		sent := sink.SendEvent(routerEvent)
		if !sent {
			applog.Warning("router: event dropped (client buffer full)",
				applog.M("event_type", routerEvent.Type))
		}
	}
}

// GetBinding retrieves the session binding for an identity (for testing/debugging).
func (r *DefaultRouter) GetBinding(identity ClientIdentity) *SessionBinding {
	return r.registry.Get(identity)
}

// ListBindings returns all current bindings (for debugging/admin).
func (r *DefaultRouter) ListBindings() []*SessionBinding {
	return r.registry.List()
}
