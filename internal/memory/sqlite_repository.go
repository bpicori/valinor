package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const dbFileName = "memory.db"

type sqliteRepository struct {
	db *sqlx.DB
}

type memoryRow struct {
	ID              string `db:"id"`
	WorkspaceDir    string `db:"workspace_dir"`
	Kind            string `db:"kind"`
	Content         string `db:"content"`
	Summary         string `db:"summary"`
	TagsJSON        string `db:"tags_json"`
	SourceSessionID string `db:"source_session_id"`
	CreatedAt       string `db:"created_at"`
	UpdatedAt       string `db:"updated_at"`
	LastAccessedAt  string `db:"last_accessed_at"`
}

// NewSQLiteRepository opens a memory repository rooted at dir.
func NewSQLiteRepository(dir string) (Repository, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}

	dbPath := filepath.Join(dir, dbFileName)
	db, err := sqlx.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}

	repo := &sqliteRepository{db: db}
	if err := repo.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repo, nil
}

func (r *sqliteRepository) initSchema() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS memories (
			id TEXT PRIMARY KEY,
			workspace_dir TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			tags_json TEXT NOT NULL DEFAULT '[]',
			source_session_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_accessed_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_memories_workspace_updated
			ON memories(workspace_dir, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_memories_workspace_kind
			ON memories(workspace_dir, kind);`,
		`CREATE INDEX IF NOT EXISTS idx_memories_workspace_last_accessed
			ON memories(workspace_dir, last_accessed_at DESC);`,
	}
	for _, stmt := range statements {
		if _, err := r.db.Exec(stmt); err != nil {
			return fmt.Errorf("init memory schema: %w", err)
		}
	}
	return nil
}

func (r *sqliteRepository) Save(ctx context.Context, memory Memory) (Memory, error) {
	if memory.ID == "" {
		memory.ID = uuid.NewString()
	}
	row, err := newMemoryRow(memory)
	if err != nil {
		return Memory{}, err
	}
	query := `
		INSERT INTO memories (
			id, workspace_dir, kind, content, summary, tags_json, source_session_id,
			created_at, updated_at, last_accessed_at
		) VALUES (
			:id, :workspace_dir, :kind, :content, :summary, :tags_json, :source_session_id,
			:created_at, :updated_at, :last_accessed_at
		)
		ON CONFLICT(id) DO UPDATE SET
			workspace_dir=excluded.workspace_dir,
			kind=excluded.kind,
			content=excluded.content,
			summary=excluded.summary,
			tags_json=excluded.tags_json,
			source_session_id=excluded.source_session_id,
			created_at=excluded.created_at,
			updated_at=excluded.updated_at,
			last_accessed_at=excluded.last_accessed_at;
	`
	if _, err := r.db.NamedExecContext(ctx, query, row); err != nil {
		return Memory{}, fmt.Errorf("save memory: %w", err)
	}
	return memory, nil
}

func (r *sqliteRepository) ListByWorkspace(ctx context.Context, workspaceDir string) ([]Memory, error) {
	query := `
		SELECT id, workspace_dir, kind, content, summary, tags_json, source_session_id,
			created_at, updated_at, last_accessed_at
		FROM memories
		WHERE workspace_dir = ?
		ORDER BY updated_at DESC, created_at DESC;
	`
	var rows []memoryRow
	if err := r.db.SelectContext(ctx, &rows, query, workspaceDir); err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}

	memories := make([]Memory, 0, len(rows))
	for _, row := range rows {
		memory, err := row.toMemory()
		if err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	return memories, nil
}

func (r *sqliteRepository) DeleteByID(ctx context.Context, id string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?;`, id)
	if err != nil {
		return false, fmt.Errorf("delete memory: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete memory rows affected: %w", err)
	}
	return rowsAffected > 0, nil
}

func (r *sqliteRepository) Touch(ctx context.Context, ids []string, touchedAt time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin touch tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `UPDATE memories SET last_accessed_at = ? WHERE id = ?;`
	timestamp := touchedAt.UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, query, timestamp, id); err != nil {
			return fmt.Errorf("touch memory %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit touch tx: %w", err)
	}
	return nil
}

func newMemoryRow(memory Memory) (memoryRow, error) {
	tagsJSON, err := json.Marshal(memory.Tags)
	if err != nil {
		return memoryRow{}, fmt.Errorf("marshal memory tags: %w", err)
	}
	return memoryRow{
		ID:              memory.ID,
		WorkspaceDir:    memory.WorkspaceDir,
		Kind:            memory.Kind,
		Content:         memory.Content,
		Summary:         memory.Summary,
		TagsJSON:        string(tagsJSON),
		SourceSessionID: memory.SourceSessionID,
		CreatedAt:       memory.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       memory.UpdatedAt.UTC().Format(time.RFC3339Nano),
		LastAccessedAt:  memory.LastAccessedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (r memoryRow) toMemory() (Memory, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if err != nil {
		return Memory{}, fmt.Errorf("parse memory created_at: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if err != nil {
		return Memory{}, fmt.Errorf("parse memory updated_at: %w", err)
	}
	lastAccessedAt, err := time.Parse(time.RFC3339Nano, r.LastAccessedAt)
	if err != nil {
		return Memory{}, fmt.Errorf("parse memory last_accessed_at: %w", err)
	}

	tags := make([]string, 0)
	if r.TagsJSON != "" {
		if err := json.Unmarshal([]byte(r.TagsJSON), &tags); err != nil {
			return Memory{}, fmt.Errorf("unmarshal memory tags: %w", err)
		}
	}

	return Memory{
		ID:              r.ID,
		WorkspaceDir:    r.WorkspaceDir,
		Kind:            r.Kind,
		Content:         r.Content,
		Summary:         r.Summary,
		Tags:            tags,
		SourceSessionID: r.SourceSessionID,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
		LastAccessedAt:  lastAccessedAt,
	}, nil
}

var _ Repository = (*sqliteRepository)(nil)
