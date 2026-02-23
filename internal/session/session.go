package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/llm"
)

// Session owns the agent and persists conversation events to the store.
type Session struct {
	meta       Metadata
	store      Store
	agent      *llm.LLM
	builder    *agentcontext.Builder
	messages   []agentcontext.Message
	config     config.Config
	persister  *timelinePersister
	mu         sync.Mutex
}

// NewSession creates a session with a dedicated timeline persister.
func NewSession(meta Metadata, store Store, ag *llm.LLM, builder *agentcontext.Builder, initialMessages []agentcontext.Message, eventBus *bus.Bus, cfg config.Config) *Session {
	return &Session{
		meta:       meta,
		store:      store,
		agent:      ag,
		builder:    builder,
		messages:   append([]agentcontext.Message{}, initialMessages...),
		config:     cfg,
		persister:  newTimelinePersister(meta.ID, store, eventBus),
	}
}

// Run starts the persistence loop. Call in a goroutine.
// Returns when ctx is cancelled.
func (s *Session) Run(ctx context.Context) {
	applog.Info("session run start", applog.M("session_id", s.meta.ID))
	s.persister.Run(ctx)
	applog.Info("session run cancelled", applog.M("session_id", s.meta.ID))
}

// HandleUserMessage appends and processes a user message through the active agent.
func (s *Session) HandleUserMessage(ctx context.Context, content string) error {
	s.mu.Lock()
	if err := s.appendUserMessage(ctx, content); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("append user message: %w", err)
	}
	s.messages = append(s.messages, agentcontext.Message{
		Role:    agentcontext.RoleUser,
		Content: content,
	})
	runCtx := s.builder.BuildRunContext(append([]agentcontext.Message{}, s.messages...))
	s.mu.Unlock()

	result, err := s.agent.Run(ctx, llm.RunRequest{Context: runCtx})
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}

	s.mu.Lock()
	s.messages = result.Messages
	s.mu.Unlock()
	return nil
}

// ResetConversation clears messages to only the base system prompt.
func (s *Session) ResetConversation() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = []agentcontext.Message{
		{
			Role:    agentcontext.RoleSystem,
			Content: s.builder.BasePrompt(),
		},
	}
}

// SwitchProviderModel updates the session's model within the same provider.
func (s *Session) SwitchProviderModel(ctx context.Context, providerName, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if providerName == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	if _, ok := s.config.Providers[providerName]; !ok {
		return fmt.Errorf("provider '%s' not found in config", providerName)
	}
	if s.meta.Provider != providerName {
		return fmt.Errorf("changing provider requires creating a new session")
	}
	if s.meta.Model == model {
		return nil
	}

	// Same provider: only update model
	s.agent.SetModel(model)
	s.meta.Model = model
	s.meta.UpdatedAt = time.Now()
	if err := s.store.SaveMetadata(ctx, s.meta); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	applog.Info("session model switched", applog.M("session_id", s.meta.ID, "model", model))
	return nil
}

func (s *Session) appendUserMessage(ctx context.Context, userMessage string) error {
	content, err := json.Marshal(userMessageContent{Content: userMessage})
	if err != nil {
		return fmt.Errorf("marshal user message: %w", err)
	}
	return s.store.AppendEvent(ctx, StoredEvent{
		Timestamp: time.Now(),
		Type:      StoredEventUserMessage,
		Content:   content,
	})
}

// Close releases resources. Call when the session is no longer needed.
func (s *Session) Close() error {
	sessionID := s.meta.ID
	applog.Debug("session close start", applog.M("session_id", sessionID))
	if s.persister != nil {
		s.persister.Close()
		s.persister = nil
	}
	s.agent = nil
	if s.store != nil {
		err := s.store.Close()
		s.store = nil
		if err != nil {
			applog.Error("session close store error", applog.M("session_id", sessionID, "error", err))
			return err
		}
		applog.Debug("session close success", applog.M("session_id", sessionID))
	}
	return nil
}

// Meta returns the session metadata.
func (s *Session) Meta() Metadata {
	return s.meta
}
