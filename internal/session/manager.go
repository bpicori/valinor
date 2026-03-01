package session

import (
	"context"
	"encoding/json"
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
	runtime      *sandbox.Runtime // shared sandbox runtime for all sessions
}

// Message history lifecycle:
// - During a live session, session appends user input to message history.
// - The full message history is persisted after each completed turn.
// - On resume, persisted messages are loaded and system prompt is rebuilt.

// NewManager creates a SessionManager that stores sessions under sessionDir and executes tools in workspaceDir.
func NewManager(sessionDir, workspaceDir string, cfg config.Config, rt *sandbox.Runtime) *Manager {
	return &Manager{sessionDir: sessionDir, workspaceDir: workspaceDir, config: cfg, runtime: rt}
}

// createParams holds parameters for internal session creation.
type createParams struct {
	Provider       string
	Model          string
	Opts           SessionOptions
	Store          Store // nil = create FileStore from sessionDir
	ClientType     string
	ClientIdentity string
	Active         bool
	Temporary      bool
	TitlePrefix    string
}

// createSession creates a new session with the given parameters.
// This is the core internal builder used by Create, CreateForClient, and CreateTemporary.
func (m *Manager) createSession(ctx context.Context, p createParams) (*Session, error) {
	providerName, model, err := m.config.ResolveProviderModel(p.Provider, p.Model)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	workspaceDir := m.workspaceDir

	now := time.Now()
	titlePrefix := p.TitlePrefix
	if titlePrefix == "" {
		titlePrefix = "Session"
	}
	meta := Metadata{
		ID:             id,
		Title:          titlePrefix + " " + id[:8],
		CreatedAt:      now,
		UpdatedAt:      now,
		WorkspaceDir:   workspaceDir,
		Provider:       providerName,
		Model:          model,
		ClientType:     p.ClientType,
		ClientIdentity: p.ClientIdentity,
		Active:         p.Active,
		Temporary:      p.Temporary,
	}

	var store Store
	if p.Store != nil {
		store = p.Store
	} else {
		dir := filepath.Join(m.sessionDir, id)
		fs, err := NewFileStore(dir)
		if err != nil {
			logger.Error("session create store error", logger.BuildMetadata("session_id", id, "error", err))
			return nil, fmt.Errorf("create store: %w", err)
		}
		store = fs
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

	// Build tools for the context (agent is created per-run)
	toolsList := tools.BuildTools(m.runtime)

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

	sess, err := NewSession(meta, store, toolsList, systemPrompt, initialMessages, m.config, p.Opts.Bus, p.Opts.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create session instance: %w", err)
	}

	return sess, nil
}

// Create creates a new session with a fresh agent and empty transcript.
// Session artifacts are stored at session_dir/<uuid>/ and tools run in the manager workspace.
func (m *Manager) Create(ctx context.Context, opts SessionOptions) (*Session, error) {
	sess, err := m.createSession(ctx, createParams{
		Provider: opts.Provider,
		Model:    opts.Model,
		Opts:     opts,
	})
	if err != nil {
		return nil, err
	}

	logger.Info("session create success",
		logger.BuildMetadata("session_id", sess.Meta().ID, "provider", sess.Meta().Provider,
			"model", sess.Meta().Model, "workspace_dir", sess.Meta().WorkspaceDir))
	return sess, nil
}

// Resume loads an existing session from storage and restores the agent's message history.
func (m *Manager) Resume(ctx context.Context, id string, opts SessionOptions) (*Session, error) {
	dir := filepath.Join(m.sessionDir, id)
	logger.Debug("session resume start", logger.BuildMetadata("session_id", id))
	store, err := NewFileStore(dir)
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

	// Build tools for the context (agent is created per-run)
	toolsList := tools.BuildTools(m.runtime)

	meta.UpdatedAt = time.Now()
	_ = store.SaveMetadata(ctx, *meta)
	_ = store.SaveMessages(ctx, msgs)

	sess, err := NewSession(*meta, store, toolsList, systemPrompt, msgs, m.config, opts.Bus, opts.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create session instance: %w", err)
	}
	logger.Info("session resume success", logger.BuildMetadata("session_id", id, "message_count", len(msgs)))
	return sess, nil
}

// readMetadataFile reads metadata directly from a session directory without creating a full store.
func readMetadataFile(dir string) (*Metadata, error) {
	metaPath := filepath.Join(dir, MetadataFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read metadata file: %w", err)
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return &meta, nil
}

// writeMetadataFile writes metadata directly to a session directory without creating a full store.
func writeMetadataFile(dir string, meta *Metadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	tmpPath := filepath.Join(dir, MetadataFile+".tmp")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write metadata temp file: %w", err)
	}

	metaPath := filepath.Join(dir, MetadataFile)
	if err := os.Rename(tmpPath, metaPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename metadata file: %w", err)
	}
	return nil
}

// GetMetadata returns metadata for a session by ID.
func (m *Manager) GetMetadata(ctx context.Context, id string) (*Metadata, error) {
	dir := filepath.Join(m.sessionDir, id)

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil // Session doesn't exist on disk
	} else if err != nil {
		return nil, fmt.Errorf("check session dir: %w", err)
	}

	return readMetadataFile(dir)
}

// GetActiveSessionForClient returns the active session for a given client identity.
// Returns nil if no active session exists for this client.
func (m *Manager) GetActiveSessionForClient(ctx context.Context, clientType, clientIdentity string, opts SessionOptions) (*Session, error) {
	dir := filepath.Join(m.sessionDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(m.sessionDir, e.Name())
		meta, err := readMetadataFile(sessionDir)
		if err != nil || meta == nil {
			continue
		}

		if meta.ClientType == clientType && meta.ClientIdentity == clientIdentity && meta.Active && !meta.Temporary {
			return m.Resume(ctx, meta.ID, opts)
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
		logger.Warning("failed to deactivate existing sessions",
			logger.BuildMetadata("client_type", clientType, "client_identity", clientIdentity, "error", err))
	}

	sess, err := m.createSession(ctx, createParams{
		Provider:       opts.Provider,
		Model:          opts.Model,
		Opts:           opts,
		ClientType:     clientType,
		ClientIdentity: clientIdentity,
		Active:         true,
	})
	if err != nil {
		return nil, err
	}

	logger.Info("session create for client success",
		logger.BuildMetadata("session_id", sess.Meta().ID, "provider", sess.Meta().Provider,
			"model", sess.Meta().Model, "client_type", clientType, "client_identity", clientIdentity))
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
	if err := writeMetadataFile(dir, meta); err != nil {
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
	sess, err := m.createSession(ctx, createParams{
		Provider:    opts.Provider,
		Model:       opts.Model,
		Opts:        opts,
		Store:       NewMemoryStore(),
		Temporary:   true,
		TitlePrefix: "Temporary",
	})
	if err != nil {
		return nil, err
	}

	logger.Info("session create temporary success",
		logger.BuildMetadata("session_id", sess.Meta().ID, "provider", sess.Meta().Provider, "model", sess.Meta().Model))
	return sess, nil
}

// deactivateClientSessions deactivates all active sessions for a given client.
func (m *Manager) deactivateClientSessions(clientType, clientIdentity string) error {
	if clientType == "" || clientIdentity == "" {
		return nil // Nothing to deactivate
	}

	dir := filepath.Join(m.sessionDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(m.sessionDir, e.Name())
		meta, err := readMetadataFile(sessionDir)
		if err != nil || meta == nil {
			continue
		}

		if meta.ClientType == clientType && meta.ClientIdentity == clientIdentity && meta.Active && !meta.Temporary {
			meta.Active = false
			meta.UpdatedAt = time.Now()

			if err := writeMetadataFile(sessionDir, meta); err != nil {
				logger.Warning("failed to deactivate session",
					logger.BuildMetadata("session_id", meta.ID, "error", err))
				continue
			}

			logger.Info("session deactivated",
				logger.BuildMetadata("session_id", meta.ID, "client_type", clientType, "client_identity", clientIdentity))
		}
	}

	return nil
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
	provider, err := factory.CreateProvider(providerCfg)
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
