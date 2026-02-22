package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/config"
	agentcontext "valinor/internal/context"
	"valinor/internal/llm"
	"valinor/internal/llm/factory"
	"valinor/internal/sandbox"
	"valinor/internal/tools"

	"github.com/google/uuid"
)

// CreateConfig holds parameters for creating a new session.
type CreateConfig struct {
	Config             config.Config
	Bus                *bus.Bus
	MaxAgentIterations int
}

// Manager manages session lifecycle.
type Manager struct {
	sessionDir   string
	workspaceDir string
}

// Message history lifecycle:
// - During a live session, session appends user input to message history.
// - Context builder compacts/builds that history before each LLM run.
// - Session events are persisted as JSONL transcript records.
// - On resume, transcript events are reconstructed into message history.

// NewManager creates a SessionManager that stores sessions under sessionDir and executes tools in workspaceDir.
func NewManager(sessionDir, workspaceDir string) *Manager {
	return &Manager{sessionDir: sessionDir, workspaceDir: workspaceDir}
}

// Create creates a new session with a fresh agent and empty transcript.
// Session artifacts are stored at session_dir/<uuid>/ and tools run in the manager workspace.
func (m *Manager) Create(ctx context.Context, cfg CreateConfig) (*Session, error) {
	providerName := cfg.Config.DefaultProvider
	providerCfg, ok := cfg.Config.Providers[providerName]
	if !ok {
		return nil, fmt.Errorf("default provider '%s' not found", providerName)
	}
	model := providerCfg.DefaultModel

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
		applog.Error("session create store error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("create store: %w", err)
	}

	if err := store.SaveMetadata(ctx, meta); err != nil {
		_ = store.Close()
		applog.Error("session create metadata save error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("save metadata: %w", err)
	}

	ctxBuilder := agentcontext.NewBuilder(agentcontext.SystemPrompt())
	snapshot, err := ctxBuilder.Build(workspaceDir)
	if err != nil {
		_ = store.Close()
		applog.Error("session create context build error", applog.M("session_id", id, "workspace_dir", workspaceDir, "error", err))
		return nil, fmt.Errorf("build context: %w", err)
	}

	// Create provider
	provider, err := createProvider(providerCfg, cfg.Bus)
	if err != nil {
		_ = store.Close()
		applog.Error("session create provider error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("create provider: %w", err)
	}

	// Create tools
	tools, err := createTools(workspaceDir)
	if err != nil {
		_ = store.Close()
		applog.Error("session create tools error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("create tools: %w", err)
	}

	ag, err := llm.New(provider, model, tools, cfg.Bus, cfg.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		applog.Error("session create agent error", applog.M("session_id", id, "model", model, "error", err))
		return nil, fmt.Errorf("create agent: %w", err)
	}

	runBuilder := agentcontext.NewBuilder(snapshot.SystemPrompt)
	initialMessages := []agentcontext.Message{
		{
			Role:    agentcontext.RoleSystem,
			Content: snapshot.SystemPrompt,
		},
	}
	sess := NewSession(meta, store, ag, runBuilder, initialMessages, cfg.Bus, cfg.Config)
	applog.Info("session create success", applog.M("session_id", id, "provider", providerName, "model", model, "workspace_dir", workspaceDir))
	return sess, nil
}

// Resume loads an existing session from storage and restores the agent's message history.
func (m *Manager) Resume(ctx context.Context, id string, cfg CreateConfig) (*Session, error) {
	dir := filepath.Join(m.sessionDir, id)
	applog.Debug("session resume start", applog.M("session_id", id))
	store, err := NewJSONLStore(dir)
	if err != nil {
		applog.Error("session resume store open error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("open store: %w", err)
	}

	meta, err := store.LoadMetadata(ctx)
	if err != nil {
		_ = store.Close()
		applog.Error("session resume metadata load error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	if meta == nil {
		_ = store.Close()
		applog.Warning("session resume not found", applog.M("session_id", id))
		return nil, fmt.Errorf("session %s not found", id)
	}

	events, err := store.LoadEvents(ctx)
	if err != nil {
		_ = store.Close()
		applog.Error("session resume events load error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("load events: %w", err)
	}

	workspaceDir := m.workspaceDir
	if meta.WorkspaceDir != "" {
		workspaceDir = meta.WorkspaceDir
	}

	// Resolve provider and model: use metadata with fallback to config defaults
	providerName := meta.Provider
	if providerName == "" {
		providerName = cfg.Config.DefaultProvider
	}
	providerCfg, ok := cfg.Config.Providers[providerName]
	if !ok {
		_ = store.Close()
		return nil, fmt.Errorf("provider '%s' not found in config", providerName)
	}
	model := meta.Model
	if model == "" {
		model = providerCfg.DefaultModel
	}
	// Persist resolved values for backward compatibility
	meta.Provider = providerName
	meta.Model = model

	ctxBuilder := agentcontext.NewBuilder(agentcontext.SystemPrompt())
	snapshot, err := ctxBuilder.Build(workspaceDir)
	if err != nil {
		_ = store.Close()
		applog.Error("session resume context build error", applog.M("session_id", id, "workspace_dir", workspaceDir, "error", err))
		return nil, fmt.Errorf("build context: %w", err)
	}

	msgs, err := ReconstructMessages(events, snapshot.SystemPrompt)
	if err != nil {
		_ = store.Close()
		applog.Error("session resume reconstruct error", applog.M("session_id", id, "event_count", len(events), "error", err))
		return nil, fmt.Errorf("reconstruct messages: %w", err)
	}

	// Create provider
	provider, err := createProvider(providerCfg, cfg.Bus)
	if err != nil {
		_ = store.Close()
		applog.Error("session resume provider error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("create provider: %w", err)
	}

	// Create tools
	tools, err := createTools(workspaceDir)
	if err != nil {
		_ = store.Close()
		applog.Error("session resume tools error", applog.M("session_id", id, "error", err))
		return nil, fmt.Errorf("create tools: %w", err)
	}

	ag, err := llm.New(provider, model, tools, cfg.Bus, cfg.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		applog.Error("session resume agent error", applog.M("session_id", id, "model", model, "error", err))
		return nil, fmt.Errorf("create agent: %w", err)
	}

	meta.UpdatedAt = time.Now()
	_ = store.SaveMetadata(ctx, *meta)

	runBuilder := agentcontext.NewBuilder(snapshot.SystemPrompt)
	sess := NewSession(*meta, store, ag, runBuilder, msgs, cfg.Bus, cfg.Config)
	applog.Info("session resume success", applog.M("session_id", id, "message_count", len(msgs), "event_count", len(events)))
	return sess, nil
}

// List returns metadata for all sessions, sorted by UpdatedAt descending.
func (m *Manager) List() ([]Metadata, error) {
	applog.Debug("session list start", nil)
	entries, err := os.ReadDir(m.sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		applog.Error("session list readdir error", applog.M("session_dir", m.sessionDir, "error", err))
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
			applog.Debug("session list skip invalid", applog.M("entry_name", e.Name(), "error", err))
			continue
		}
		meta, err := store.LoadMetadata(context.Background())
		_ = store.Close()
		if err != nil || meta == nil {
			applog.Debug("session list skip invalid", applog.M("entry_name", e.Name(), "error", err))
			continue
		}
		list = append(list, *meta)
	}

	// Sort by UpdatedAt descending
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].UpdatedAt.After(list[i].UpdatedAt) {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	applog.Debug("session list success", applog.M("count", len(list)))
	return list, nil
}

// GetMetadata returns metadata for a session by ID.
func (m *Manager) GetMetadata(ctx context.Context, id string) (*Metadata, error) {
	dir := filepath.Join(m.sessionDir, id)
	store, err := NewJSONLStore(dir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	defer store.Close()
	return store.LoadMetadata(ctx)
}

// LoadEvents returns persisted transcript events for a given session ID.
func (m *Manager) LoadEvents(ctx context.Context, id string) ([]StoredEvent, error) {
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

	events, err := store.LoadEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}
	return events, nil
}

// Delete removes a session directory and all its data.
func (m *Manager) Delete(id string) error {
	applog.Info("session delete start", applog.M("session_id", id))
	dir := filepath.Join(m.sessionDir, id)
	if err := os.RemoveAll(dir); err != nil {
		applog.Error("session delete error", applog.M("session_id", id, "error", err))
		return err
	}
	applog.Info("session delete success", applog.M("session_id", id))
	return nil
}

// createProvider is a helper that delegates to the factory.
func createProvider(cfg config.ProviderConfig, b *bus.Bus) (llm.Provider, error) {
	return factory.CreateProvider(cfg, b)
}

// createTools is a helper that creates tools for the given workspace.
func createTools(workspaceDir string) ([]llm.Tool, error) {
	rt, err := sandbox.NewRuntime(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("create runtime: %w", err)
	}
	return tools.BuildTools(rt), nil
}
