// Package router provides the internal API boundary for routing commands and events.
package router

import (
	"fmt"
	"sync"

	"valinor/internal/applog"
	"valinor/internal/session"
)

// SessionBinding holds the mapping between a client identity and their session.
type SessionBinding struct {
	Identity  ClientIdentity
	Session   *session.Session
	SessionID string
	PublicKey string // session:<client_type>:<short_id>
}

// SessionRegistry manages identity-to-session mappings in-memory.
// It provides thread-safe operations for registering, looking up, and removing bindings.
type SessionRegistry struct {
	mu       sync.RWMutex
	bindings map[string]*SessionBinding // key: identity key (Type:Identity)
}

// NewSessionRegistry creates a new empty session registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{
		bindings: make(map[string]*SessionBinding),
	}
}

// Register creates a new binding between an identity and a session.
// If a binding already exists for this identity, it returns the existing binding.
func (r *SessionRegistry) Register(identity ClientIdentity, sess *session.Session) *SessionBinding {
	key := identity.Key()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if binding already exists
	if existing, ok := r.bindings[key]; ok {
		applog.Debug("session registry: reusing existing binding",
			applog.BuildMetadata("identity_key", key, "session_id", existing.SessionID))
		return existing
	}

	// Create new binding
	meta := sess.Meta()
	binding := &SessionBinding{
		Identity:  identity,
		Session:   sess,
		SessionID: meta.ID,
		PublicKey: identity.SessionKey(meta.ID),
	}

	r.bindings[key] = binding

	applog.Info("session registry: new binding created",
		applog.BuildMetadata("identity_key", key,
			"session_id", meta.ID,
			"public_key", binding.PublicKey,
			"provider", meta.Provider,
			"model", meta.Model))

	return binding
}

// Get retrieves a binding by identity key.
func (r *SessionRegistry) Get(identity ClientIdentity) *SessionBinding {
	key := identity.Key()

	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.bindings[key]
}

// GetByKey retrieves a binding by the raw identity key string.
func (r *SessionRegistry) GetByKey(key string) *SessionBinding {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.bindings[key]
}

// Remove deletes a binding by identity key.
// Returns true if a binding was removed, false if none existed.
func (r *SessionRegistry) Remove(identity ClientIdentity) bool {
	key := identity.Key()

	r.mu.Lock()
	defer r.mu.Unlock()

	if binding, ok := r.bindings[key]; ok {
		delete(r.bindings, key)
		applog.Info("session registry: binding removed",
			applog.BuildMetadata("identity_key", key, "session_id", binding.SessionID))
		return true
	}

	return false
}

// RemoveBySessionID removes all bindings associated with a given session ID.
func (r *SessionRegistry) RemoveBySessionID(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	removed := 0
	for key, binding := range r.bindings {
		if binding.SessionID == sessionID {
			delete(r.bindings, key)
			applog.Info("session registry: binding removed (session deleted)",
				applog.BuildMetadata("identity_key", key, "session_id", sessionID))
			removed++
		}
	}

	return removed
}

// List returns all current bindings (for debugging/admin purposes).
func (r *SessionRegistry) List() []*SessionBinding {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*SessionBinding, 0, len(r.bindings))
	for _, binding := range r.bindings {
		result = append(result, binding)
	}

	return result
}

// Count returns the number of active bindings.
func (r *SessionRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.bindings)
}

// ValidateClientIdentity checks if a client identity is valid 
// Returns an error if the identity is invalid.
func ValidateClientIdentity(identity ClientIdentity) error {
	if identity.Type == "" {
		return fmt.Errorf("client_type is required")
	}

	if !ValidClientTypes[identity.Type] {
		return fmt.Errorf("invalid client_type: %s", identity.Type)
	}

	if identity.Identity == "" {
		return fmt.Errorf("client_identity is required")
	}

	return nil
}
