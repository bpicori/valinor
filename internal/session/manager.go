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

// CreateSessionOptions holds parameters for creating/resuming a session.
// For non-temporary sessions, ClientType and ClientIdentity are required.
type CreateSessionOptions struct {
	// Bus is the event bus for publishing session events. Required.
	Bus *event_bus.Bus
	// MaxAgentIterations limits the number of agent turns per request.
	MaxAgentIterations int
	// Provider overrides the default provider when set.
	Provider string
	// Model overrides the default model when set.
	Model string
	// ClientType identifies the client kind (e.g., "cursor", "vscode", "tui").
	// Required for non-temporary sessions.
	ClientType string
	// ClientIdentity uniquely identifies the client instance.
	// Required for non-temporary sessions.
	ClientIdentity string
	// Temporary creates an in-memory-only session without persistence.
	// Temporary sessions do not require client binding.
	Temporary bool
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

// Create creates a new session with a fresh agent and empty transcript.
//
// For non-temporary sessions (opts.Temporary == false), ClientType and ClientIdentity
// are required. The session will be persisted to disk and marked as active for the client.
//
// For temporary sessions (opts.Temporary == true), client binding is optional and
// the session exists only in memory.
//
// Note: For non-temporary sessions, callers should deactivate any existing active
// session for the same client before calling Create, using DeactivateClientSessions.
func (m *Manager) Create(ctx context.Context, opts CreateSessionOptions) (*Session, error) {
	// Validate: non-temporary sessions require client binding
	if !opts.Temporary {
		if opts.ClientType == "" || opts.ClientIdentity == "" {
			return nil, fmt.Errorf("non-temporary sessions require ClientType and ClientIdentity")
		}
	}

	providerName, model, err := m.config.ResolveProviderModel(opts.Provider, opts.Model)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	workspaceDir := m.workspaceDir

	now := time.Now()
	titlePrefix := "Session"
	if opts.Temporary {
		titlePrefix = "Temporary"
	}

	// Active is true only for non-temporary sessions with valid client binding
	active := !opts.Temporary && opts.ClientType != "" && opts.ClientIdentity != ""

	meta := Metadata{
		ID:             id,
		Title:          titlePrefix + " " + id[:8],
		CreatedAt:      now,
		UpdatedAt:      now,
		WorkspaceDir:   workspaceDir,
		Provider:       providerName,
		Model:          model,
		ClientType:     opts.ClientType,
		ClientIdentity: opts.ClientIdentity,
		Active:         active,
		Temporary:      opts.Temporary,
	}

	var store Store
	if opts.Temporary {
		store = NewMemoryStore()
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

	sess, err := NewSession(meta, store, toolsList, systemPrompt, initialMessages, m.config, opts.Bus, opts.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create session instance: %w", err)
	}

	logAttrs := []any{
		"session_id", sess.Meta().ID,
		"provider", sess.Meta().Provider,
		"model", sess.Meta().Model,
		"temporary", sess.Meta().Temporary,
	}
	if !sess.Meta().Temporary {
		logAttrs = append(logAttrs, "client_type", sess.Meta().ClientType, "client_identity", sess.Meta().ClientIdentity)
	}
	logger.Info("session create success", logger.BuildMetadata(logAttrs...))
	return sess, nil
}

// Resume loads an existing session from storage and restores the agent's message history.
func (m *Manager) Resume(ctx context.Context, id string, opts CreateSessionOptions) (*Session, error) {
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
func (m *Manager) GetActiveSessionForClient(ctx context.Context, clientType, clientIdentity string, opts CreateSessionOptions) (*Session, error) {
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
	if err := m.DeactivateClientSessions(meta.ClientType, meta.ClientIdentity); err != nil {
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

// DeactivateClientSessions deactivates all active sessions for a given client.
// This should be called by the caller before creating a new non-temporary session
// to ensure proper hand-off behavior (only one active session per client).
func (m *Manager) DeactivateClientSessions(clientType, clientIdentity string) error {
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
