package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"valinor/internal/bus"
	agentcontext "valinor/internal/context"
	"valinor/internal/llm"

	"github.com/google/uuid"
)

// CreateConfig holds parameters for creating a new session.
type CreateConfig struct {
	Model              string
	APIKey             string
	BaseURL            string
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
	id := uuid.New().String()
	dir := filepath.Join(m.sessionDir, id)
	workspaceDir := m.workspaceDir

	now := time.Now()
	meta := Metadata{
		ID:           id,
		Title:        "Session " + id[:8],
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        cfg.Model,
		WorkspaceDir: workspaceDir,
	}

	store, err := NewJSONLStore(dir)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	if err := store.SaveMetadata(ctx, meta); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("save metadata: %w", err)
	}

	ctxBuilder := agentcontext.NewBuilder(agentcontext.SystemPrompt())
	snapshot, err := ctxBuilder.Build(workspaceDir)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("build context: %w", err)
	}

	ag, err := llm.New(cfg.APIKey, cfg.BaseURL, cfg.Model, workspaceDir, cfg.Bus, cfg.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create agent: %w", err)
	}

	runBuilder := agentcontext.NewBuilder(snapshot.SystemPrompt)
	initialMessages := []agentcontext.Message{
		{
			Role:    agentcontext.RoleSystem,
			Content: snapshot.SystemPrompt,
		},
	}
	sess := NewSession(meta, store, ag, runBuilder, initialMessages, cfg.Bus)
	return sess, nil
}

// Resume loads an existing session from storage and restores the agent's message history.
func (m *Manager) Resume(ctx context.Context, id string, cfg CreateConfig) (*Session, error) {
	dir := filepath.Join(m.sessionDir, id)
	store, err := NewJSONLStore(dir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	meta, err := store.LoadMetadata(ctx)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	if meta == nil {
		_ = store.Close()
		return nil, fmt.Errorf("session %s not found", id)
	}

	events, err := store.LoadEvents(ctx)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("load events: %w", err)
	}

	workspaceDir := m.workspaceDir
	if meta.WorkspaceDir != "" {
		workspaceDir = meta.WorkspaceDir
	}

	ctxBuilder := agentcontext.NewBuilder(agentcontext.SystemPrompt())
	snapshot, err := ctxBuilder.Build(workspaceDir)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("build context: %w", err)
	}

	msgs, err := ReconstructMessages(events, snapshot.SystemPrompt)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("reconstruct messages: %w", err)
	}

	ag, err := llm.New(cfg.APIKey, cfg.BaseURL, meta.Model, workspaceDir, cfg.Bus, cfg.MaxAgentIterations)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("create agent: %w", err)
	}

	meta.UpdatedAt = time.Now()
	_ = store.SaveMetadata(ctx, *meta)

	runBuilder := agentcontext.NewBuilder(snapshot.SystemPrompt)
	sess := NewSession(*meta, store, ag, runBuilder, msgs, cfg.Bus)
	return sess, nil
}

// List returns metadata for all sessions, sorted by UpdatedAt descending.
func (m *Manager) List() ([]Metadata, error) {
	entries, err := os.ReadDir(m.sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
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
			continue
		}
		meta, err := store.LoadMetadata(context.Background())
		_ = store.Close()
		if err != nil || meta == nil {
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
	return list, nil
}

// Delete removes a session directory and all its data.
func (m *Manager) Delete(id string) error {
	dir := filepath.Join(m.sessionDir, id)
	return os.RemoveAll(dir)
}
