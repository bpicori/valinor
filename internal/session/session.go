package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"valinor/internal/applog"
	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/llm"
)

// Metadata holds session metadata persisted in metadata.json.
type Metadata struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	WorkspaceDir string    `json:"workspace_dir"`
	Provider     string    `json:"provider,omitempty"` // Config key for the LLM provider
	Model        string    `json:"model,omitempty"`    // Model ID for the session
}

// Session owns the agent and persists conversation messages to the store.
type Session struct {
	meta         Metadata
	store        Store
	agent        *llm.LLM
	agentContext agentcontext.Context
	config       config.Config
	mu           sync.Mutex
}

// NewSession creates a session with one owned context state.
func NewSession(meta Metadata, store Store, ag *llm.LLM, systemPrompt string, initialMessages []agentcontext.Message, cfg config.Config) *Session {
	return &Session{
		meta:         meta,
		store:        store,
		agent:        ag,
		agentContext: agentcontext.NewContext(systemPrompt, initialMessages),
		config:       cfg,
	}
}

// HandleUserMessage appends and processes a user message through the active agent.
func (s *Session) HandleUserMessage(ctx context.Context, content string) error {
	s.mu.Lock()
	s.agentContext.AppendUserMessage(content)
	runMessages := s.agentContext.Messages()
	s.mu.Unlock()

	result, err := s.agent.Run(ctx, llm.RunRequest{Messages: runMessages})
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}

	s.mu.Lock()
	s.agentContext.ReplaceMessages(result.Messages)
	persistMessages := s.agentContext.Messages()
	s.mu.Unlock()
	if err := s.store.SaveMessages(ctx, persistMessages); err != nil {
		return fmt.Errorf("save messages: %w", err)
	}
	return nil
}

// ResetConversation clears messages to only the base system prompt.
func (s *Session) ResetConversation() {
	s.mu.Lock()
	s.agentContext.Reset()
	persistMessages := s.agentContext.Messages()
	s.mu.Unlock()
	if err := s.store.SaveMessages(context.Background(), persistMessages); err != nil {
		applog.Error("session reset save messages error", applog.BuildMetadata("session_id", s.meta.ID, "error", err))
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
	applog.Info("session model switched", applog.BuildMetadata("session_id", s.meta.ID, "model", model))
	return nil
}

// Close releases resources. Call when the session is no longer needed.
func (s *Session) Close() error {
	sessionID := s.meta.ID
	applog.Debug("session close start", applog.BuildMetadata("session_id", sessionID))
	s.agent = nil
	if s.store != nil {
		err := s.store.Close()
		s.store = nil
		if err != nil {
			applog.Error("session close store error", applog.BuildMetadata("session_id", sessionID, "error", err))
			return err
		}
		applog.Debug("session close success", applog.BuildMetadata("session_id", sessionID))
	}
	return nil
}

// Meta returns the session metadata.
func (s *Session) Meta() Metadata {
	return s.meta
}
