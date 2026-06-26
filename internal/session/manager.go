package session

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/llm"
	"valinor/internal/llm/factory"
	"valinor/internal/logger"
	"github.com/google/uuid"
)

// CreateSessionOptions holds parameters for creating/resuming a session.
// For non-temporary sessions, ClientType and ClientIdentity are required.
type CreateSessionOptions struct {
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
	// WorkspaceDir overrides the default workspace for this session when set.
	WorkspaceDir string
}

// Manager manages session lifecycle.
type Manager struct {
	sessionDir    string
	workspaceDir  string
	metadataStore MetadataRepository
}

// Message history lifecycle:
// - During a live session, session appends user input to message history.
// - The full message history is persisted after each completed turn.
// - On resume, persisted messages are loaded and system prompt is rebuilt.

// NewManager creates a SessionManager that stores sessions under sessionDir and executes tools in workspaceDir.
func NewManager(sessionDir, workspaceDir string) (*Manager, error) {
	metadataStore, err := NewSQLiteMetadataRepository(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("create session metadata repository: %w", err)
	}
	return &Manager{
		sessionDir:    sessionDir,
		workspaceDir:  workspaceDir,
		metadataStore: metadataStore,
	}, nil
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

	providerName, model, err := config.Must().ResolveProviderModel(opts.Provider, opts.Model)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	workspaceDir := m.workspaceDir
	if opts.WorkspaceDir != "" {
		workspaceDir = opts.WorkspaceDir
	}

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
		fs, err := NewFileStore(dir, m.metadataStore)
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

	agentCtx, err := agentcontext.New(workspaceDir, providerName, model)
	if err != nil {
		_ = store.Close()
		logger.Error("session create context build error", logger.BuildMetadata("session_id", id, "workspace_dir", workspaceDir, "error", err))
		return nil, fmt.Errorf("build context: %w", err)
	}
	if err := store.SaveMessages(ctx, agentCtx.Messages()); err != nil {
		_ = store.Close()
		logger.Error("session create save messages error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("save messages: %w", err)
	}

	sess, err := NewSession(meta, store, agentCtx)
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
	store, err := NewFileStore(dir, m.metadataStore)
	if err != nil {
		logger.Error("session resume store open error", logger.BuildMetadata("session_id", id, "error", err))
		return nil, fmt.Errorf("open store: %w", err)
	}

	meta, err := m.metadataStore.GetByID(ctx, id)
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

	providerName, model, err := config.Must().ResolveProviderModel(meta.Provider, meta.Model)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	// Persist resolved provider/model values.
	meta.Provider = providerName
	meta.Model = model

	agentCtx, err := agentcontext.Resume(workspaceDir, providerName, model, messages)
	if err != nil {
		_ = store.Close()
		logger.Error("session resume context build error", logger.BuildMetadata("session_id", id, "workspace_dir", workspaceDir, "error", err))
		return nil, fmt.Errorf("build context: %w", err)
	}

	meta.UpdatedAt = time.Now()
	_ = store.SaveMetadata(ctx, *meta)
	_ = store.SaveMessages(ctx, agentCtx.Messages())

	sess, err := NewSession(*meta, store, agentCtx)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create session instance: %w", err)
	}
	logger.Info("session resume success", logger.BuildMetadata("session_id", id, "message_count", len(agentCtx.Messages())))
	return sess, nil
}

// GetMetadata returns metadata for a session by ID.
func (m *Manager) GetMetadata(ctx context.Context, id string) (*Metadata, error) {
	meta, err := m.metadataStore.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get metadata: %w", err)
	}
	return meta, nil
}

// ListAllMetadata returns metadata for all sessions (excluding temporary ones).
func (m *Manager) ListAllMetadata(ctx context.Context) ([]Metadata, error) {
	result, err := m.metadataStore.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list metadata: %w", err)
	}
	return result, nil
}

// GetActiveSessionForClient returns the active session for a given client identity.
// Returns nil if no active session exists for this client.
func (m *Manager) GetActiveSessionForClient(ctx context.Context, clientType, clientIdentity string, opts CreateSessionOptions) (*Session, error) {
	meta, err := m.metadataStore.GetActiveForClient(ctx, clientType, clientIdentity)
	if err != nil {
		return nil, fmt.Errorf("get active session metadata: %w", err)
	}
	if meta == nil {
		return nil, nil
	}
	return m.Resume(ctx, meta.ID, opts)
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

	if err := m.metadataStore.SetSessionActive(context.Background(), sessionID); err != nil {
		return fmt.Errorf("set session active: %w", err)
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

	if err := m.metadataStore.DeactivateClientSessions(context.Background(), clientType, clientIdentity); err != nil {
		return fmt.Errorf("deactivate client sessions: %w", err)
	}

	return nil
}

// ListProviderModels fetches available models for the given provider.
func (m *Manager) ListProviderModels(ctx context.Context, providerName string) ([]llm.ModelInfo, error) {
	if providerName == "" {
		return nil, fmt.Errorf("provider is required")
	}
	providerCfg, ok := config.Must().Providers[providerName]
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
