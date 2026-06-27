package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/llm"
	"valinor/internal/llm/factory"
	"valinor/internal/logger"
	agenttools "valinor/internal/tools"
)

// Metadata holds session metadata persisted in SQLite.
type Metadata struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	WorkspaceDir   string    `json:"workspace_dir"`
	Provider       string    `json:"provider,omitempty"`        // Config key for the LLM provider
	Model          string    `json:"model,omitempty"`           // Model ID for the session
	ClientType     string    `json:"client_type,omitempty"`     // Client type that owns this session (e.g., "tui", "telegram")
	ClientIdentity string    `json:"client_identity,omitempty"` // Client identity that owns this session
	Active         bool      `json:"active,omitempty"`          // Whether this is the active session for the client
	Temporary      bool      `json:"temporary,omitempty"`       // Whether this is a temporary (non-persisted) session
}

// Session owns the conversation context and persists messages to the store.
// The LLM agent is created fresh for each run.
type Session struct {
	meta         Metadata
	store        Store
	agentContext agentcontext.Context
	lastUsage    llm.Usage // last real token usage from API response
	mu           sync.Mutex
}

// RunInteractionOptions controls how an interaction is executed.
type RunInteractionOptions struct {
	EmitEvents bool
}

// NewSession creates a session with the provided agent context.
func NewSession(meta Metadata, store Store, agentCtx agentcontext.Context) (*Session, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	return &Session{
		meta:         meta,
		store:        store,
		agentContext: agentCtx,
	}, nil
}

// RunInteraction appends a user message and processes it through a fresh agent.
func (s *Session) RunInteraction(ctx context.Context, requestID string, message string) error {
	_, err := s.RunInteractionWithOptions(ctx, requestID, message, RunInteractionOptions{
		EmitEvents: true,
	})
	return err
}

// RunInteractionWithOptions appends a user message and processes it through a fresh agent.
func (s *Session) RunInteractionWithOptions(ctx context.Context, requestID string, message string, opts RunInteractionOptions) (llm.RunResult, error) {
	s.mu.Lock()
	runData := s.agentContext.PrepareRun(message)
	s.mu.Unlock()

	providerCfg := config.Must().Providers[s.meta.Provider]
	provider, err := factory.CreateProvider(providerCfg)
	if err != nil {
		return llm.RunResult{}, fmt.Errorf("create provider: %w", err)
	}

	agent := llm.New(provider, s.meta.Model, config.Must().MaxAgentIterations)
	compressor := llm.NewCompressor(provider, s.meta.Model)
	agenttools.BindRunContext(runData.RunContext, s.meta.ID, requestID, opts.EmitEvents)

	result, err := agent.Run(ctx, llm.RunRequest{
		Messages:   runData.Messages,
		Tools:      runData.Tools,
		SessionID:  s.meta.ID,
		RequestID:  requestID,
		EmitEvents: opts.EmitEvents,
		Compressor: compressor,
	})
	if err != nil {
		return llm.RunResult{}, fmt.Errorf("agent run: %w", err)
	}

	s.mu.Lock()
	s.lastUsage = result.Usage
	persistMessages := s.agentContext.CompleteRun(result.Messages)
	s.mu.Unlock()
	if err := s.store.SaveMessages(ctx, persistMessages); err != nil {
		return llm.RunResult{}, fmt.Errorf("save messages: %w", err)
	}
	return result, nil
}

// Tools returns the tools available in this session.
func (s *Session) Tools() []agenttools.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentContext.Tools()
}

// RunContext returns shared per-run metadata for session tools.
func (s *Session) RunContext() *agenttools.RunContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentContext.RunContext()
}

// SetTools replaces the tools available in this session.
func (s *Session) SetTools(tools []agenttools.Tool, runContext *agenttools.RunContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentContext.SetTools(tools, runContext)
}

// ResetConversation clears messages to only the base system prompt.
func (s *Session) ResetConversation(ctx context.Context) error {
	s.mu.Lock()
	s.agentContext.Reset()
	s.lastUsage = llm.Usage{} // reset cached usage
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

	builtTools, runContext, err := agenttools.BuildTools(s.meta.WorkspaceDir, s.meta.Provider, model)
	if err != nil {
		return fmt.Errorf("build tools: %w", err)
	}
	s.agentContext.SetTools(builtTools, runContext)

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

// ContextTokenCount returns the token count of the current conversation context.
// It prefers real prompt token usage from the last API response when available,
// because context size is the number of input tokens sent back to the model.
// The second return value indicates whether the count is estimated (true) or
// real (false).
func (s *Session) ContextTokenCount() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastUsage.PromptTokens > 0 {
		return s.lastUsage.PromptTokens, false
	}
	if s.lastUsage.TotalTokens > 0 {
		return s.lastUsage.TotalTokens, false
	}
	messages := s.agentContext.Messages()
	return llm.EstimatePromptTokens(messages), true
}