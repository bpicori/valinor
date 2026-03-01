package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/event_bus"
	"valinor/internal/llm"
	"valinor/internal/llm/factory"
	"valinor/internal/logger"
	"valinor/internal/tools"
)

// Metadata holds session metadata persisted in metadata.json.
type Metadata struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	WorkspaceDir   string    `json:"workspace_dir"`
	Provider       string    `json:"provider,omitempty"` // Config key for the LLM provider
	Model          string    `json:"model,omitempty"`    // Model ID for the session
	ClientType     string    `json:"client_type,omitempty"`     // Client type that owns this session (e.g., "tui", "telegram")
	ClientIdentity string    `json:"client_identity,omitempty"` // Client identity that owns this session
	Active         bool      `json:"active,omitempty"`          // Whether this is the active session for the client
	Temporary      bool      `json:"temporary,omitempty"`       // Whether this is a temporary (non-persisted) session
}

// Session owns the conversation context and persists messages to the store.
// The LLM agent is created fresh for each run.
type Session struct {
	meta            Metadata
	store           Store
	agentContext    agentcontext.Context
	config          config.Config
	bus             *event_bus.Bus
	maxIterations   int
	mu              sync.Mutex
}

// NewSession creates a session with one owned context state.
func NewSession(meta Metadata, store Store, tools []tools.Tool, systemPrompt string, initialMessages []agentcontext.Message, cfg config.Config, bus *event_bus.Bus, maxIterations int) (*Session, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if bus == nil {
		return nil, fmt.Errorf("event bus is required")
	}
	return &Session{
		meta:          meta,
		store:         store,
		agentContext:  agentcontext.NewContext(systemPrompt, initialMessages, tools),
		config:        cfg,
		bus:           bus,
		maxIterations: maxIterations,
	}, nil
}

// buildAgent creates a fresh LLM agent for a single run.
func (s *Session) buildAgent() (*llm.LLM, error) {
	providerCfg := s.config.Providers[s.meta.Provider]
	provider, err := factory.CreateProvider(providerCfg)
	if err != nil {
		return nil, fmt.Errorf("create provider: %w", err)
	}

	ag, err := llm.New(provider, s.meta.Model, s.bus, s.maxIterations)
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return ag, nil
}

// RunInteraction appends a user message and processes it through a fresh agent.
func (s *Session) RunInteraction(ctx context.Context, requestID string, content string) error {
	s.mu.Lock()
	s.agentContext.AppendUserMessage(content)
	runMessages := s.agentContext.Messages()
	runTools := s.agentContext.Tools()
	s.mu.Unlock()

	// Build a fresh agent for this run
	agent, err := s.buildAgent()
	if err != nil {
		return fmt.Errorf("build agent: %w", err)
	}

	result, err := agent.Run(ctx, llm.RunRequest{
		Messages:  runMessages,
		Tools:     runTools,
		SessionID: s.meta.ID,
		RequestID: requestID,
	})
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
func (s *Session) ResetConversation(ctx context.Context) error {
	s.mu.Lock()
	s.agentContext.Reset()
	persistMessages := s.agentContext.Messages()
	s.mu.Unlock()
	if err := s.store.SaveMessages(ctx, persistMessages); err != nil {
		logger.Error("session reset save messages error", logger.BuildMetadata("session_id", s.meta.ID, "error", err))
		return fmt.Errorf("save messages: %w", err)
	}
	return nil
}

// SetModel updates the session's model within the current provider.
// Note: Changing providers requires creating a new session.
// The model is stored in metadata and used when building the agent for each run.
func (s *Session) SetModel(ctx context.Context, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if model == "" {
		return fmt.Errorf("model is required")
	}
	if s.meta.Model == model {
		return nil
	}

	s.meta.Model = model
	s.meta.UpdatedAt = time.Now()
	if err := s.store.SaveMetadata(ctx, s.meta); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	logger.Info("session model switched", logger.BuildMetadata("session_id", s.meta.ID, "model", model))
	return nil
}

// Close releases resources. Call when the session is no longer needed.
func (s *Session) Close() error {
	sessionID := s.meta.ID
	logger.Debug("session close start", logger.BuildMetadata("session_id", sessionID))

	if s.store != nil {
		err := s.store.Close()
		s.store = nil
		if err != nil {
			logger.Error("session close store error", logger.BuildMetadata("session_id", sessionID, "error", err))
			return err
		}
	}

	logger.Debug("session close success", logger.BuildMetadata("session_id", sessionID))
	return nil
}

// Meta returns the session metadata.
func (s *Session) Meta() Metadata {
	return s.meta
}
