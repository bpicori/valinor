package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/event_bus"
	"valinor/internal/llm"
	"valinor/internal/llm/factory"
	"valinor/internal/logger"
	"valinor/internal/sandbox"
	"valinor/internal/tools"

	"github.com/google/uuid"
)

// SessionOptions holds parameters for creating/resuming a session.
type SessionOptions struct {
	Bus                *event_bus.Bus
	MaxAgentIterations int
	// Provider and Model override manager config defaults when set.
	Provider string
	Model    string
}

// Manager manages session lifecycle.
type Manager struct {
	sessionDir   string
	workspaceDir string
	config       config.Config
}

// Message history lifecycle:
// - During a live session, session appends user input to message history.
// - The full message history is persisted after each completed turn.
// - On resume, persisted messages are loaded and system prompt is rebuilt.

// NewManager creates a SessionManager that stores sessions under sessionDir and executes tools in workspaceDir.
func NewManager(sessionDir, workspaceDir string, cfg config.Config) *Manager {
	return &Manager{sessionDir: sessionDir, workspaceDir: workspaceDir, config: cfg}
}

// Create creates a new session with a fresh agent and empty transcript.
// Session artifacts are stored at session_dir/<uuid>/ and tools run in the manager workspace.
func (m *Manager) Create(ctx context.Context, opts SessionOptions) (*Session, error) {
	providerName, model, err := m.config.ResolveProviderModel(opts.Provider, opts.Model)
	if err != nil {
		return nil, err
	}
	providerCfg := m.config.Providers[providerName]

	id := uuid.New().String()
	dir := filepath.Join(m.sessionDir, id)
	workspaceDir := m.workspaceDir

	now := time.Now()
	meta := Metadata{
		ID:           id,
		Title:        "Session " + id[:8],
		CreatedAt:    now,
		UpdatedAt:    now,
		WorkspaceDir: workspaceDir,
		Provider:     providerName,
		Model:        model,
	}

	store, err := NewJSONLStore(dir)
	if err != nil {
		logger.Error("session create store error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("create store: %w", err)
	}

	if err := store.SaveMetadata(ctx, meta); err != nil {
		_ = store.Close()
		logger.Error("session create metadata save error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("save metadata: %w", err)
	}

	systemPrompt, err := agentcontext.BuildSystemPrompt(workspaceDir)
	if err != nil {
		_ = store.Close()
		logger.Error("session create context build error", logger.BuildMetadata("session_id", id, "workspace_dir", workspaceDir, "error", err))
		return nil, fmt.Errorf("build context: %w", err)
	}

	ag, err := buildAgent(workspaceDir, providerCfg, model, opts.Bus, opts.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		logger.Error("session create agent error", logger.BuildMetadata("session_id", id, "model", model, "error", err))
		return nil, err
	}

	initialMessages := []agentcontext.Message{
		{
			Role:    agentcontext.RoleSystem,
			Content: systemPrompt,
		},
	}
	if err := store.SaveMessages(ctx, initialMessages); err != nil {
		_ = store.Close()
		logger.Error("session create save messages error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("save messages: %w", err)
	}
	sess, err := NewSession(meta, store, ag, systemPrompt, initialMessages, m.config)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create session instance: %w", err)
	}
	logger.Info("session create success", logger.BuildMetadata("session_id", id, "provider", providerName, "model", model, "workspace_dir", workspaceDir))
	return sess, nil
}

// Resume loads an existing session from storage and restores the agent's message history.
func (m *Manager) Resume(ctx context.Context, id string, opts SessionOptions) (*Session, error) {
	dir := filepath.Join(m.sessionDir, id)
	logger.Debug("session resume start", logger.BuildMetadata("session_id", id))
	store, err := NewJSONLStore(dir)
	if err != nil {
		logger.Error("session resume store open error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("open store: %w", err)
	}

	meta, err := store.LoadMetadata(ctx)
	if err != nil {
		_ = store.Close()
		logger.Error("session resume metadata load error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	if meta == nil {
		_ = store.Close()
		logger.Warning("session resume not found", logger.BuildMetadata("session_id", id))
		return nil, fmt.Errorf("session %s not found", id)
	}

	messages, err := store.LoadMessages(ctx)
	if err != nil {
		_ = store.Close()
		logger.Error("session resume messages load error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("load messages: %w", err)
	}

	workspaceDir := m.workspaceDir
	if meta.WorkspaceDir != "" {
		workspaceDir = meta.WorkspaceDir
	}

	providerName, model, err := m.config.ResolveProviderModel(meta.Provider, meta.Model)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	providerCfg := m.config.Providers[providerName]
	// Persist resolved values for backward compatibility
	meta.Provider = providerName
	meta.Model = model

	systemPrompt, err := agentcontext.BuildSystemPrompt(workspaceDir)
	if err != nil {
		_ = store.Close()
		logger.Error("session resume context build error", logger.BuildMetadata("session_id", id, "workspace_dir", workspaceDir, "error", err))
		return nil, fmt.Errorf("build context: %w", err)
	}

	msgs := normalizeSystemPrompt(messages, systemPrompt)
	ag, err := buildAgent(workspaceDir, providerCfg, model, opts.Bus, opts.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		logger.Error("session resume agent error", logger.BuildMetadata("session_id", id, "model", model, "error", err))
		return nil, err
	}

	meta.UpdatedAt = time.Now()
	_ = store.SaveMetadata(ctx, *meta)
	_ = store.SaveMessages(ctx, msgs)

	sess, err := NewSession(*meta, store, ag, systemPrompt, msgs, m.config)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create session instance: %w", err)
	}
	logger.Info("session resume success", logger.BuildMetadata("session_id", id, "message_count", len(msgs)))
	return sess, nil
}

// List returns metadata for all sessions, sorted by UpdatedAt descending.
func (m *Manager) List() ([]Metadata, error) {
	logger.Debug("session list start", nil)
	entries, err := os.ReadDir(m.sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		logger.Error("session list readdir error", logger.BuildMetadata("session_dir", m.sessionDir, "error", err))
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	var list []Metadata
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(m.sessionDir, e.Name())
		store, err := NewJSONLStore(dir)
		if err != nil {
			logger.Debug("session list skip invalid", logger.BuildMetadata("entry_name", e.Name(), "error", err))
			continue
		}
		meta, err := store.LoadMetadata(context.Background())
		_ = store.Close()
		if err != nil || meta == nil {
			logger.Debug("session list skip invalid", logger.BuildMetadata("entry_name", e.Name(), "error", err))
			continue
		}
		list = append(list, *meta)
	}

	// Sort by UpdatedAt descending
	sort.Slice(list, func(i, j int) bool {
		return list[j].UpdatedAt.Before(list[i].UpdatedAt)
	})
	logger.Debug("session list success", logger.BuildMetadata("count", len(list)))
	return list, nil
}

// GetMetadata returns metadata for a session by ID.
func (m *Manager) GetMetadata(ctx context.Context, id string) (*Metadata, error) {
	dir := filepath.Join(m.sessionDir, id)

	// Check if directory exists before creating store to avoid creating
	// empty directories for temporary/non-existent sessions
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil // Session doesn't exist on disk
	} else if err != nil {
		return nil, fmt.Errorf("check session dir: %w", err)
	}

	store, err := NewJSONLStore(dir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	defer store.Close()
	return store.LoadMetadata(ctx)
}

// LoadMessages returns persisted messages for a given session ID.
func (m *Manager) LoadMessages(ctx context.Context, id string) ([]agentcontext.Message, error) {
	dir := filepath.Join(m.sessionDir, id)
	store, err := NewJSONLStore(dir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	meta, err := store.LoadMetadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	if meta == nil {
		return nil, fmt.Errorf("session %s not found", id)
	}

	messages, err := store.LoadMessages(ctx)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	return messages, nil
}

// Delete removes a session directory and all its data.
func (m *Manager) Delete(id string) error {
	logger.Info("session delete start", logger.BuildMetadata("session_id", id))
	dir := filepath.Join(m.sessionDir, id)
	if err := os.RemoveAll(dir); err != nil {
		logger.Error("session delete error", logger.BuildMetadata("session_id", id, "error", err))
		return err
	}
	logger.Info("session delete success", logger.BuildMetadata("session_id", id))
	return nil
}

// GetActiveSessionForClient returns the active session for a given client identity.
// Returns nil if no active session exists for this client.
func (m *Manager) GetActiveSessionForClient(ctx context.Context, clientType, clientIdentity string) (*Session, error) {
	sessions, err := m.List()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	for _, meta := range sessions {
		if meta.ClientType == clientType && meta.ClientIdentity == clientIdentity && meta.Active && !meta.Temporary {
			return m.Resume(ctx, meta.ID, SessionOptions{})
		}
	}

	return nil, nil // No active session found
}

// CreateForClient creates a new session bound to a specific client identity.
// The session is marked as active for this client, and any previous active session
// for the same client is deactivated.
func (m *Manager) CreateForClient(ctx context.Context, clientType, clientIdentity string, opts SessionOptions) (*Session, error) {
	// First, deactivate any existing active session for this client
	if err := m.deactivateClientSessions(clientType, clientIdentity); err != nil {
		logger.Warning("failed to deactivate existing sessions", logger.BuildMetadata("client_type", clientType, "client_identity", clientIdentity, "error", err))
	}

	providerName, model, err := m.config.ResolveProviderModel(opts.Provider, opts.Model)
	if err != nil {
		return nil, err
	}
	providerCfg := m.config.Providers[providerName]

	id := uuid.New().String()
	dir := filepath.Join(m.sessionDir, id)
	workspaceDir := m.workspaceDir

	now := time.Now()
	meta := Metadata{
		ID:             id,
		Title:          "Session " + id[:8],
		CreatedAt:      now,
		UpdatedAt:      now,
		WorkspaceDir:   workspaceDir,
		Provider:       providerName,
		Model:          model,
		ClientType:     clientType,
		ClientIdentity: clientIdentity,
		Active:         true,
		Temporary:      false,
	}

	store, err := NewJSONLStore(dir)
	if err != nil {
		logger.Error("session create store error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("create store: %w", err)
	}

	if err := store.SaveMetadata(ctx, meta); err != nil {
		_ = store.Close()
		logger.Error("session create metadata save error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("save metadata: %w", err)
	}

	systemPrompt, err := agentcontext.BuildSystemPrompt(workspaceDir)
	if err != nil {
		_ = store.Close()
		logger.Error("session create context build error", logger.BuildMetadata("session_id", id, "workspace_dir", workspaceDir, "error", err))
		return nil, fmt.Errorf("build context: %w", err)
	}

	ag, err := buildAgent(workspaceDir, providerCfg, model, opts.Bus, opts.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		logger.Error("session create agent error", logger.BuildMetadata("session_id", id, "model", model, "error", err))
		return nil, err
	}

	initialMessages := []agentcontext.Message{
		{
			Role:    agentcontext.RoleSystem,
			Content: systemPrompt,
		},
	}
	if err := store.SaveMessages(ctx, initialMessages); err != nil {
		_ = store.Close()
		logger.Error("session create save messages error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("save messages: %w", err)
	}

	sess, err := NewSession(meta, store, ag, systemPrompt, initialMessages, m.config)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create session instance: %w", err)
	}

	logger.Info("session create for client success",
		logger.BuildMetadata("session_id", id, "provider", providerName, "model", model,
			"client_type", clientType, "client_identity", clientIdentity))
	return sess, nil
}

// SetSessionActive marks a session as active for its client, deactivating any other
// active session for the same client.
func (m *Manager) SetSessionActive(sessionID string) error {
	// Load the target session to get its client info
	meta, err := m.GetMetadata(context.Background(), sessionID)
	if err != nil {
		return fmt.Errorf("get session metadata: %w", err)
	}
	if meta == nil {
		return fmt.Errorf("session %s not found", sessionID)
	}

	// Deactivate other sessions for this client
	if err := m.deactivateClientSessions(meta.ClientType, meta.ClientIdentity); err != nil {
		return fmt.Errorf("deactivate existing sessions: %w", err)
	}

	// Activate this session
	meta.Active = true
	meta.UpdatedAt = time.Now()

	dir := filepath.Join(m.sessionDir, sessionID)
	store, err := NewJSONLStore(dir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	if err := store.SaveMetadata(context.Background(), *meta); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}

	logger.Info("session activated",
		logger.BuildMetadata("session_id", sessionID, "client_type", meta.ClientType, "client_identity", meta.ClientIdentity))
	return nil
}

// CreateTemporary creates a new temporary session without disk persistence.
// Used for ephemeral executions like background jobs. Events from temporary sessions
// are broadcast to all connected clients.
func (m *Manager) CreateTemporary(ctx context.Context, opts SessionOptions) (*Session, error) {
	providerName, model, err := m.config.ResolveProviderModel(opts.Provider, opts.Model)
	if err != nil {
		return nil, err
	}
	providerCfg := m.config.Providers[providerName]

	id := uuid.New().String()
	workspaceDir := m.workspaceDir

	now := time.Now()
	meta := Metadata{
		ID:           id,
		Title:        "Temporary " + id[:8],
		CreatedAt:    now,
		UpdatedAt:    now,
		WorkspaceDir: workspaceDir,
		Provider:     providerName,
		Model:        model,
		Temporary:    true,  // Mark as temporary
		Active:       false, // Temporary sessions are not "active" in the client sense
	}

	// Use in-memory store instead of JSONLStore
	store := NewMemoryStore()

	systemPrompt, err := agentcontext.BuildSystemPrompt(workspaceDir)
	if err != nil {
		logger.Error("session create temporary context build error",
			logger.BuildMetadata("session_id", id, "workspace_dir", workspaceDir, "error", err))
		return nil, fmt.Errorf("build context: %w", err)
	}

	ag, err := buildAgent(workspaceDir, providerCfg, model, opts.Bus, opts.MaxAgentIterations)
	if err != nil {
		logger.Error("session create temporary agent error",
			logger.BuildMetadata("session_id", id, "model", model, "error", err))
		return nil, err
	}

	initialMessages := []agentcontext.Message{
		{
			Role:    agentcontext.RoleSystem,
			Content: systemPrompt,
		},
	}
	if err := store.SaveMessages(ctx, initialMessages); err != nil {
		return nil, fmt.Errorf("save messages: %w", err)
	}

	sess, err := NewSession(meta, store, ag, systemPrompt, initialMessages, m.config)
	if err != nil {
		return nil, fmt.Errorf("create session instance: %w", err)
	}

	logger.Info("session create temporary success",
		logger.BuildMetadata("session_id", id, "provider", providerName, "model", model))
	return sess, nil
}

// deactivateClientSessions deactivates all active sessions for a given client.
func (m *Manager) deactivateClientSessions(clientType, clientIdentity string) error {
	if clientType == "" || clientIdentity == "" {
		return nil // Nothing to deactivate
	}

	sessions, err := m.List()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	for _, meta := range sessions {
		if meta.ClientType == clientType && meta.ClientIdentity == clientIdentity && meta.Active && !meta.Temporary {
			meta.Active = false
			meta.UpdatedAt = time.Now()

			dir := filepath.Join(m.sessionDir, meta.ID)
			store, err := NewJSONLStore(dir)
			if err != nil {
				logger.Warning("failed to open store for deactivation",
					logger.BuildMetadata("session_id", meta.ID, "error", err))
				continue
			}

			if err := store.SaveMetadata(context.Background(), meta); err != nil {
				logger.Warning("failed to deactivate session",
					logger.BuildMetadata("session_id", meta.ID, "error", err))
			}
			store.Close()

			logger.Info("session deactivated",
				logger.BuildMetadata("session_id", meta.ID, "client_type", clientType, "client_identity", clientIdentity))
		}
	}

	return nil
}

// createProvider is a helper that delegates to the factory.
func createProvider(cfg config.ProviderConfig) (llm.Provider, error) {
	return factory.CreateProvider(cfg)
}

// ListProviderModels fetches available models for the given provider.
func (m *Manager) ListProviderModels(ctx context.Context, providerName string) ([]llm.ModelInfo, error) {
	if providerName == "" {
		return nil, fmt.Errorf("provider is required")
	}
	providerCfg, ok := m.config.Providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider '%s' not found in config", providerName)
	}
	provider, err := createProvider(providerCfg)
	if err != nil {
		return nil, fmt.Errorf("create provider: %w", err)
	}
	models, err := provider.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	sort.Slice(models, func(i, j int) bool {
		left := models[i].ID
		if models[i].DisplayName != "" {
			left = models[i].DisplayName
		}
		right := models[j].ID
		if models[j].DisplayName != "" {
			right = models[j].DisplayName
		}
		return left < right
	})
	return models, nil
}

// createTools is a helper that creates tools for the given workspace.
func createTools(workspaceDir string) ([]llm.Tool, error) {
	rt, err := sandbox.NewRuntime(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("create runtime: %w", err)
	}
	return tools.BuildTools(rt), nil
}

func buildAgent(workspaceDir string, providerCfg config.ProviderConfig, model string, b *event_bus.Bus, maxIterations int) (*llm.LLM, error) {
	provider, err := createProvider(providerCfg)
	if err != nil {
		return nil, fmt.Errorf("create provider: %w", err)
	}
	availableTools, err := createTools(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("create tools: %w", err)
	}
	ag, err := llm.New(provider, model, availableTools, b, maxIterations)
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return ag, nil
}

func normalizeSystemPrompt(messages []agentcontext.Message, systemPrompt string) []agentcontext.Message {
	base := agentcontext.CloneMessages(messages)
	if len(base) == 0 {
		return []agentcontext.Message{{
			Role:    agentcontext.RoleSystem,
			Content: systemPrompt,
		}}
	}

	if base[0].Role == agentcontext.RoleSystem {
		base[0].Content = systemPrompt
		return base
	}

	return append([]agentcontext.Message{{
		Role:    agentcontext.RoleSystem,
		Content: systemPrompt,
	}}, base...)
}
