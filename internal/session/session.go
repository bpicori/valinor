package session

import (
	"context"
	"fmt"
	"strings"
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

	// Auto-promote a descriptive title from the first real user message if we still have
	// the boring default "Session/Temporary xxxxxxxx" title. This makes long-lived
	// Telegram (and other) sessions much more usable without extra user work.
	if isDefaultTitle(s.meta.Title) && len(s.agentContext.Messages()) <= 2 {
		if candidate := deriveTitle(message); candidate != "" && candidate != s.meta.Title {
			s.meta.Title = candidate
			s.meta.UpdatedAt = time.Now()
			// Persist title outside the hot path; ignore error (non-fatal for the turn)
			_ = s.store.SaveMetadata(ctx, s.meta)
			logger.Info("session auto-titled", logger.BuildMetadata("session_id", s.meta.ID, "title", candidate))
		}
	}
	s.mu.Unlock()

	runMessages := runData.Messages
	if dirStore, ok := s.store.(interface{ SessionDir() string }); ok {
		hydrated, err := HydrateMessages(dirStore.SessionDir(), runData.Messages)
		if err != nil {
			return llm.RunResult{}, fmt.Errorf("hydrate messages: %w", err)
		}
		runMessages = hydrated
	}

	providerCfg := config.Must().Providers[s.meta.Provider]
	provider, err := factory.CreateProvider(providerCfg)
	if err != nil {
		return llm.RunResult{}, fmt.Errorf("create provider: %w", err)
	}

	cfg := config.Must()
	agent := llm.New(provider, s.meta.Model, cfg.MaxAgentIterations, cfg.Context.MaxOutputTokens)
	compressor := llm.NewCompressor(provider, s.meta.Model, cfg.Context.WindowTokens)
	agenttools.BindRunContext(runData.RunContext, s.meta.ID, requestID, opts.EmitEvents)

	result, err := agent.Run(ctx, llm.RunRequest{
		Messages:          runMessages,
		Tools:             runData.Tools,
		SessionID:         s.meta.ID,
		RequestID:         requestID,
		EmitEvents:        opts.EmitEvents,
		Compressor:        compressor,
		PriorPromptTokens: s.lastPromptTokens(),
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

// SetTitle sets a human-readable title for the session and persists it.
// Titles improve resume UX and client listings (e.g. Telegram /status).
func (s *Session) SetTitle(ctx context.Context, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("title must not be empty")
	}
	if s.meta.Title == title {
		return nil
	}

	s.meta.Title = title
	s.meta.UpdatedAt = time.Now()
	if err := s.store.SaveMetadata(ctx, s.meta); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	logger.Info("session title updated", logger.BuildMetadata("session_id", s.meta.ID, "title", title))
	return nil
}

// isDefaultTitle reports whether the title is still one of the generated placeholders.
func isDefaultTitle(t string) bool {
	return strings.HasPrefix(t, "Session ") || strings.HasPrefix(t, "Temporary ")
}

// deriveTitle produces a short, human-friendly title from a user message.
// It takes the first line/sentence and truncates sensibly.
func deriveTitle(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	// Cut at first line break or sentence end within reasonable length.
	cut := -1
	for _, sep := range []string{"\n", ".", "!", "?"} {
		if i := strings.Index(msg, sep); i > 0 && (cut == -1 || i < cut) {
			cut = i
		}
	}
	if cut > 0 && cut < 70 {
		msg = msg[:cut]
	}
	if len(msg) > 60 {
		msg = strings.TrimSpace(msg[:57]) + "..."
	}
	// Collapse internal whitespace
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return ""
	}
	// Title case first word for niceness (very light touch)
	if len(msg) > 1 {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return msg
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

// lastPromptTokens returns the provider-reported prompt size of the previous turn.
func (s *Session) lastPromptTokens() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsage.PromptTokens
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