package gateway

import (
	"context"
	"fmt"
	"sync"

	"valinor/internal/event"
	"valinor/internal/event_bus"
	"valinor/internal/logger"
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
	bus         *event_bus.Bus
	runtime     *session.Runtime
	manager     *session.Manager
	sessionOpts session.SessionOptions

	// clientConsumers maps identity key to the connected consumer
	mu              sync.RWMutex
	clientConsumers map[string]ClientEventConsumer // key: identity key (type:identity)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	busSub *event_bus.Subscription
}

// NewRouter creates a new router instance.
func NewRouter(
	b *event_bus.Bus,
	runtime *session.Runtime,
	manager *session.Manager,
	sessionOpts session.SessionOptions,
) *Router {
	return &Router{
		bus:             b,
		runtime:         runtime,
		manager:         manager,
		sessionOpts:     sessionOpts,
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
		event.CronJobResult,
	)

	// Start the event forwarding goroutine
	r.wg.Add(1)
	go r.eventForwardingLoop()

	logger.Info("router started", nil)
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

	logger.Info("router stopped", logger.BuildMetadata("consumers", len(consumers)))
}

// SubscribeClient registers a client event consumer and returns/creates their session.
func (r *Router) SubscribeClient(ctx context.Context, identity ClientIdentity, consumer ClientEventConsumer) (*session.Session, error) {
	// Validate identity
	if err := ValidateClientIdentity(identity); err != nil {
		return nil, fmt.Errorf("invalid client identity: %w", err)
	}

	key := identity.Key()

	r.mu.Lock()
	oldConsumer := r.clientConsumers[key]
	r.clientConsumers[key] = consumer
	r.mu.Unlock()

	// Close old consumer outside lock to avoid deadlock
	if oldConsumer != nil && oldConsumer != consumer {
		oldConsumer.Close()
		logger.Info("router: client replaced existing connection",
			logger.BuildMetadata("identity_key", key))
	}

	// Get or create active session for this client
	sess, err := r.manager.GetActiveSessionForClient(ctx, identity.Type, identity.Identity)
	if err != nil {
		return nil, fmt.Errorf("get active session: %w", err)
	}

	if sess == nil {
		// Create a new session for this client
		sess, err = r.manager.CreateForClient(ctx, identity.Type, identity.Identity, r.sessionOpts)
		if err != nil {
			return nil, fmt.Errorf("create session for client: %w", err)
		}
	}

	// Ensure session has a worker in the runtime
	sess, err = r.runtime.EnsureActiveSession(sess)
	if err != nil {
		return nil, fmt.Errorf("ensure active session: %w", err)
	}

	// Send session attached event
	meta := sess.Meta()
	consumer.SendEvent(Event{
		Type: event.SessionAttached,
		Payload: SessionAttachedPayload{
			SessionKey: identity.SessionKey(meta.ID),
			Provider:   meta.Provider,
			Model:      meta.Model,
		},
	})

	logger.Info("router: client subscribed",
		logger.BuildMetadata("identity_key", key, "session_id", meta.ID, "new_session", oldConsumer == nil))

	return sess, nil
}

// UnsubscribeClient unregisters a client event consumer.
func (r *Router) UnsubscribeClient(identity ClientIdentity, consumer ClientEventConsumer) {
	key := identity.Key()

	r.mu.Lock()
	currentConsumer, ok := r.clientConsumers[key]
	if ok {
		if consumer != nil && currentConsumer != consumer {
			r.mu.Unlock()
			logger.Debug("router: stale unsubscribe ignored",
				logger.BuildMetadata("identity_key", key))
			return
		}
		delete(r.clientConsumers, key)
	}
	r.mu.Unlock()

	logger.Debug("router: client unsubscribed",
		logger.BuildMetadata("identity_key", key))
}

// HandleCommand processes a command from a client.
func (r *Router) HandleCommand(ctx context.Context, cmd Command) error {
	// Validate we have a valid identity
	if err := ValidateClientIdentity(cmd.Identity); err != nil {
		return fmt.Errorf("invalid command identity: %w", err)
	}

	key := cmd.Identity.Key()

	// Get active session for this client
	sess, err := r.manager.GetActiveSessionForClient(ctx, cmd.Identity.Type, cmd.Identity.Identity)
	if err != nil {
		return fmt.Errorf("get active session: %w", err)
	}
	if sess == nil {
		return fmt.Errorf("no active session found for identity %s", key)
	}

	sessionID := sess.Meta().ID

	logger.Debug("router: handling command",
		logger.BuildMetadata("identity_key", key,
			"command_type", cmd.Type,
			"session_id", sessionID))

	// Handle session creation directly
	if cmd.Type == CommandCreateNewSession {
		payload, ok := cmd.Payload.(CreateNewSessionPayload)
		if !ok {
			return fmt.Errorf("invalid create_new_session payload")
		}

		newSessionID, newSession, err := r.runtime.HandleCreateSessionRequest(
			sessionID, payload.Provider, payload.Model, payload.Force)
		if err != nil {
			return err
		}

		// Mark the new session as active for this client
		if err := r.manager.SetSessionActive(newSessionID); err != nil {
			logger.Warning("router: failed to activate new session",
				logger.BuildMetadata("error", err, "session_id", newSessionID))
		}

		// Send session attached event to the client
		r.mu.RLock()
		consumer := r.clientConsumers[key]
		r.mu.RUnlock()

		if consumer != nil {
			consumer.SendEvent(Event{
				Type: event.SessionAttached,
				Payload: SessionAttachedPayload{
					SessionKey: cmd.Identity.SessionKey(newSessionID),
					Provider:   newSession.Meta().Provider,
					Model:      newSession.Meta().Model,
				},
			})
		}

		logger.Info("router: session switched",
			logger.BuildMetadata("identity_key", key, "old_session_id", sessionID, "new_session_id", newSessionID))
		return nil
	}

	// Dispatch to the specific session worker
	eventType := event.EventType(cmd.Type)
	if err := r.runtime.HandleCommandForSession(sessionID, cmd.RequestID, eventType, cmd.Payload); err != nil {
		return fmt.Errorf("handle command for session %s: %w", sessionID, err)
	}

	return nil
}

// eventForwardingLoop forwards bus events to connected clients.
func (r *Router) eventForwardingLoop() {
	defer r.wg.Done()

	for {
		select {
		case <-r.ctx.Done():
			logger.Debug("router: event forwarding loop exiting", nil)
			return
		case ev, ok := <-r.busSub.Ch():
			if !ok {
				logger.Debug("router: event channel closed", nil)
				return
			}
			r.forwardEventToClients(ev)
		}
	}
}

// forwardEventToClients routes events to the appropriate clients.
// Events from temporary sessions or with no owner are broadcast to all clients.
// Events from owned sessions are sent only to the owner.
func (r *Router) forwardEventToClients(be event.Event) {
	routerEvent := FromBusEvent(be)
	if routerEvent.Type == "" {
		return
	}

	var consumers []ClientEventConsumer

	if be.SessionID == "" {
		// Broadcast: events without session ID go to all clients
		r.mu.RLock()
		consumers = make([]ClientEventConsumer, 0, len(r.clientConsumers))
		for _, consumer := range r.clientConsumers {
			consumers = append(consumers, consumer)
		}
		r.mu.RUnlock()

		if routerEvent.Type == event.CronJobResult {
			logger.Debug("router: broadcasting cron job result",
				logger.BuildMetadata("consumer_count", len(consumers)))
		}
	} else {
		// Look up session metadata to find the owner
		meta, err := r.manager.GetMetadata(r.ctx, be.SessionID)
		if err != nil {
			logger.Warning("router: failed to get session metadata for routing",
				logger.BuildMetadata("session_id", be.SessionID, "error", err))
			// Fall back to broadcast
			r.mu.RLock()
			consumers = make([]ClientEventConsumer, 0, len(r.clientConsumers))
			for _, consumer := range r.clientConsumers {
				consumers = append(consumers, consumer)
			}
			r.mu.RUnlock()
		} else if meta == nil {
			// Session not found - broadcast
			r.mu.RLock()
			consumers = make([]ClientEventConsumer, 0, len(r.clientConsumers))
			for _, consumer := range r.clientConsumers {
				consumers = append(consumers, consumer)
			}
			r.mu.RUnlock()
		} else if meta.Temporary || meta.ClientType == "" || meta.ClientIdentity == "" {
			// Temporary session or no owner - broadcast to all
			r.mu.RLock()
			consumers = make([]ClientEventConsumer, 0, len(r.clientConsumers))
			for _, consumer := range r.clientConsumers {
				consumers = append(consumers, consumer)
			}
			r.mu.RUnlock()
		} else {
			// Owned session - send only to the owner
			identityKey := meta.ClientType + ":" + meta.ClientIdentity
			r.mu.RLock()
			consumer, ok := r.clientConsumers[identityKey]
			r.mu.RUnlock()
			if ok {
				consumers = []ClientEventConsumer{consumer}
			} else {
				// Owner not connected - drop event
				return
			}
		}
	}

	// Send to clients (non-blocking)
	for _, consumer := range consumers {
		sent := consumer.SendEvent(routerEvent)
		if !sent {
			logger.Warning("router: event dropped (client buffer full)",
				logger.BuildMetadata("event_type", routerEvent.Type))
		}
	}
}

// GetConsumer returns the consumer for a specific identity (for testing/debugging).
func (r *Router) GetConsumer(identity ClientIdentity) ClientEventConsumer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.clientConsumers[identity.Key()]
}

// ListConsumers returns all connected consumer identities (for debugging/admin).
func (r *Router) ListConsumers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.clientConsumers))
	for key := range r.clientConsumers {
		result = append(result, key)
	}
	return result
}
