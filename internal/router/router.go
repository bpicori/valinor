// Package router provides the internal API boundary for routing commands and events.
package router

import (
	"context"
	"fmt"
	"sync"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/event"
	"valinor/internal/session"
)

// ClientEventConsumer is the interface for sending events to a specific client.
// The gateway implements this to forward events to connected WebSocket clients.
type ClientEventConsumer interface {
	// SendEvent sends an event to the client. Non-blocking: drops event if client buffer full.
	SendEvent(event Event) bool
	// Close closes the client connection.
	Close()
}

// Router is the central routing component that manages client connections,
// session bindings, and routes commands/events between clients and the session core.
type Router struct {
	bus         *bus.Bus
	runtime     *session.Runtime
	manager     *session.Manager
	sessionOpts session.SessionOptions
	identityMap *session.IdentityMapStore

	registry *SessionRegistry

	mu              sync.RWMutex
	clientConsumers map[string]ClientEventConsumer // key: identity key

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
) *Router {
	return &Router{
		bus:             b,
		runtime:         runtime,
		manager:         manager,
		sessionOpts:     sessionOpts,
		identityMap:     identityMap,
		registry:        NewSessionRegistry(),
		clientConsumers: make(map[string]ClientEventConsumer),
	}
}

// Start starts the router's event forwarding loop.
func (r *Router) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ctx != nil {
		return // already started
	}

	r.ctx, r.cancel = context.WithCancel(ctx)

	// Subscribe to all agent events from the bus
	r.busSub = r.bus.Subscribe(
		event.AgentContentDelta,
		event.AgentContentFull,
		event.AgentThinking,
		event.AgentStepTokens,
		event.ContextTokens,
		event.AgentRefusal,
		event.AgentToolCallStart,
		event.AgentToolCallDone,
		event.AgentDone,
		event.AgentError,
		event.AgentCancelled,
		event.ProviderModelsResult,
	)

	// Start the event forwarding goroutine
	r.wg.Add(1)
	go r.eventForwardingLoop()

	applog.Info("router started", nil)
}

// Stop stops the router and cleans up resources.
func (r *Router) Stop() {
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

	// Copy and clear consumers under lock, then close outside lock to avoid lock
	// re-entry when consumer.Close() calls back into UnsubscribeClient.
	r.mu.Lock()
	consumers := make([]ClientEventConsumer, 0, len(r.clientConsumers))
	for key, consumer := range r.clientConsumers {
		consumers = append(consumers, consumer)
		delete(r.clientConsumers, key)
	}
	r.mu.Unlock()

	for _, consumer := range consumers {
		consumer.Close()
	}

	applog.Info("router stopped", applog.BuildMetadata("bindings", r.registry.Count()))
}

// SubscribeClient registers a client event consumer and returns/creates their session binding.
func (r *Router) SubscribeClient(ctx context.Context, identity ClientIdentity, consumer ClientEventConsumer) (*SessionBinding, error) {
	// Validate identity
	if err := ValidateClientIdentity(identity); err != nil {
		return nil, fmt.Errorf("invalid client identity: %w", err)
	}

	key := identity.Key()

	// Check if we already have an in-memory binding
	if binding := r.registry.GetByKey(key); binding != nil {
		activeSession, err := r.runtime.EnsureActiveSession(binding.Session)
		if err != nil {
			return nil, fmt.Errorf("ensure active session: %w", err)
		}
		binding.Session = activeSession

		// Register the new consumer (replaces any existing consumer for this identity).
		// Close previous consumer outside lock to avoid deadlock/re-entrancy issues.
		var oldConsumer ClientEventConsumer
		r.mu.Lock()
		oldConsumer = r.clientConsumers[key]
		r.clientConsumers[key] = consumer
		r.mu.Unlock()
		if oldConsumer != nil && oldConsumer != consumer {
			oldConsumer.Close()
		}

		applog.Info("router: client reconnected to in-memory session",
			applog.BuildMetadata("identity_key", key, "session_id", binding.SessionID))

		// Send session attached event
		consumer.SendEvent(Event{
			Type: event.SessionAttached,
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

	sess, err = r.runtime.EnsureActiveSession(sess)
	if err != nil {
		return nil, fmt.Errorf("ensure active session: %w", err)
	}

	// Register the binding
	binding := r.registry.Register(identity, sess)

	// Update identity map with current session
	if r.identityMap != nil {
		if err := r.identityMap.Set(key, sess.Meta().ID, identity.Type); err != nil {
			applog.Warning("router: failed to save identity mapping",
				applog.BuildMetadata("error", err, "identity_key", key))
			// Continue - this is not fatal
		}
	}

	// Store the consumer
	r.mu.Lock()
	r.clientConsumers[key] = consumer
	r.mu.Unlock()

	// Send session attached event
	consumer.SendEvent(Event{
		Type: event.SessionAttached,
		Payload: SessionAttachedPayload{
			SessionKey: binding.PublicKey,
			Provider:   binding.Session.Meta().Provider,
			Model:      binding.Session.Meta().Model,
		},
	})

	if isNew {
		applog.Info("router: new client subscribed with new session",
			applog.BuildMetadata("identity_key", key, "session_id", binding.SessionID))
	} else {
		applog.Info("router: new client subscribed with resumed session",
			applog.BuildMetadata("identity_key", key, "session_id", binding.SessionID))
	}

	return binding, nil
}

// UnsubscribeClient unregisters a client event consumer.
func (r *Router) UnsubscribeClient(identity ClientIdentity, consumer ClientEventConsumer) {
	key := identity.Key()

	r.mu.Lock()
	currentConsumer, ok := r.clientConsumers[key]
	if ok {
		if consumer != nil && currentConsumer != consumer {
			r.mu.Unlock()
			applog.Debug("router: stale unsubscribe ignored",
				applog.BuildMetadata("identity_key", key))
			return
		}
		delete(r.clientConsumers, key)
	}
	r.mu.Unlock()

	applog.Debug("router: client unsubscribed",
		applog.BuildMetadata("identity_key", key))
}

// HandleCommand processes a command from a client.
func (r *Router) HandleCommand(ctx context.Context, cmd Command) error {
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
		applog.BuildMetadata("identity_key", key,
			"command_type", cmd.Type,
			"session_id", binding.SessionID))

	// Handle session-creation directly so identity mapping tracks the switched session.
	if cmd.Type == CommandCreateNewSession {
		payload, ok := cmd.Payload.(CreateNewSessionPayload)
		if !ok {
			return fmt.Errorf("invalid create_new_session payload")
		}

		newSessionID, err := r.runtime.HandleCreateSessionRequest(payload.Provider, payload.Model)
		if err != nil {
			return err
		}

		activeSession, err := r.runtime.ActiveSession()
		if err != nil {
			return fmt.Errorf("fetch active session after switch: %w", err)
		}
		updatedBinding, updated := r.registry.UpdateSession(cmd.Identity, activeSession)
		if !updated {
			applog.Warning("router: failed to update binding after session switch",
				applog.BuildMetadata("identity_key", key, "session_id", newSessionID))
		} else {
			r.mu.RLock()
			consumer := r.clientConsumers[key]
			r.mu.RUnlock()
			if consumer != nil {
				consumer.SendEvent(Event{
					Type: event.SessionAttached,
					Payload: SessionAttachedPayload{
						SessionKey: updatedBinding.PublicKey,
						Provider:   updatedBinding.Session.Meta().Provider,
						Model:      updatedBinding.Session.Meta().Model,
					},
				})
			}
		}
		if r.identityMap != nil {
			if err := r.identityMap.Set(key, newSessionID, cmd.Identity.Type); err != nil {
				applog.Warning("router: failed to update identity mapping after session switch",
					applog.BuildMetadata("error", err, "identity_key", key, "session_id", newSessionID))
			}
		}

		return nil
	}

	// Convert router command to bus event and publish
	busEvent := r.commandToBusEvent(cmd)
	r.bus.Publish(busEvent)

	return nil
}

// commandToBusEvent converts a router command to a bus event.
// Since both router and bus now use the unified event package, this is a simple mapping.
func (r *Router) commandToBusEvent(cmd Command) event.Event {
	return event.Event{
		Type:    event.EventType(cmd.Type),
		Payload: cmd.Payload,
	}
}

// getOrCreateSessionForIdentity gets an existing session from the identity map or creates a new one.
// Returns the session, a boolean indicating if it's new, and any error.
func (r *Router) getOrCreateSessionForIdentity(ctx context.Context, identity ClientIdentity) (*session.Session, bool, error) {
	key := identity.Key()

	// Check identity map for existing session
	if r.identityMap != nil {
		if mapping := r.identityMap.Get(key); mapping != nil {
			// Try to resume the existing session
			sess, err := r.manager.Resume(ctx, mapping.SessionID, r.sessionOpts)
			if err == nil {
				applog.Info("router: resumed session from identity map",
					applog.BuildMetadata("identity_key", key, "session_id", mapping.SessionID))
				return sess, false, nil
			}
			applog.Warning("router: failed to resume mapped session, creating new",
				applog.BuildMetadata("identity_key", key, "session_id", mapping.SessionID, "error", err))
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
func (r *Router) eventForwardingLoop() {
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
func (r *Router) forwardEventToClients(be event.Event) {
	// Convert to router event (now a no-op since both use event.Event)
	routerEvent := FromBusEvent(be)
	if routerEvent.Type == "" {
		// Unknown event type, skip
		return
	}

	r.mu.RLock()
	consumers := make([]ClientEventConsumer, 0, len(r.clientConsumers))
	for _, consumer := range r.clientConsumers {
		consumers = append(consumers, consumer)
	}
	r.mu.RUnlock()

	// Send to all clients (non-blocking)
	for _, consumer := range consumers {
		sent := consumer.SendEvent(routerEvent)
		if !sent {
			applog.Warning("router: event dropped (client buffer full)",
				applog.BuildMetadata("event_type", routerEvent.Type))
		}
	}
}

// GetBinding retrieves the session binding for an identity (for testing/debugging).
func (r *Router) GetBinding(identity ClientIdentity) *SessionBinding {
	return r.registry.Get(identity)
}

// ListBindings returns all current bindings (for debugging/admin).
func (r *Router) ListBindings() []*SessionBinding {
	return r.registry.List()
}
