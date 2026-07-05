package session

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const metadataDBFile = "metadata.db"

// MetadataRepository persists non-temporary session metadata.
type MetadataRepository interface {
	Upsert(ctx context.Context, meta Metadata) error
	GetByID(ctx context.Context, id string) (*Metadata, error)
	ListAll(ctx context.Context) ([]Metadata, error)
	GetActiveForClient(ctx context.Context, clientType, clientIdentity string) (*Metadata, error)
	DeactivateClientSessions(ctx context.Context, clientType, clientIdentity string) error
	SetSessionActive(ctx context.Context, sessionID string) error
	Delete(ctx context.Context, id string) error
}

// SQLiteMetadataRepository stores metadata in a SQLite database.
type SQLiteMetadataRepository struct {
	db *sqlx.DB
}

type metadataRow struct {
	ID             string `db:"id"`
	Title          string `db:"title"`
	CreatedAt      string `db:"created_at"`
	UpdatedAt      string `db:"updated_at"`
	WorkspaceDir   string `db:"workspace_dir"`
	Provider       string `db:"provider"`
	Model          string `db:"model"`
	ClientType     string `db:"client_type"`
	ClientIdentity string `db:"client_identity"`
	Active         int    `db:"active"`
	Temporary      int    `db:"temporary"`
}

type deactivateClientSessionsParams struct {
	UpdatedAt      string `db:"updated_at"`
	ClientType     string `db:"client_type"`
	ClientIdentity string `db:"client_identity"`
}

type activateSessionParams struct {
	UpdatedAt      string `db:"updated_at"`
	ClientType     string `db:"client_type"`
	ClientIdentity string `db:"client_identity"`
	SessionID      string `db:"session_id"`
}

type activationTargetRow struct {
	ClientType     string `db:"client_type"`
	ClientIdentity string `db:"client_identity"`
	Temporary      int    `db:"temporary"`
}

// NewSQLiteMetadataRepository opens <sessionDir>/metadata.db and ensures schema.
func NewSQLiteMetadataRepository(sessionDir string) (*SQLiteMetadataRepository, error) {
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	dbPath := filepath.Join(sessionDir, metadataDBFile)
	db, err := sqlx.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite metadata db: %w", err)
	}

	repo := &SQLiteMetadataRepository{db: db}
	if err := repo.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repo, nil
}

func (r *SQLiteMetadataRepository) initSchema() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			workspace_dir TEXT NOT NULL,
			provider TEXT,
			model TEXT,
			client_type TEXT,
			client_identity TEXT,
			active INTEGER NOT NULL DEFAULT 0,
			temporary INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_client_active
			ON sessions(client_type, client_identity, active, temporary);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_updated_at
			ON sessions(updated_at DESC);`,
	}
	for _, stmt := range statements {
		if _, err := r.db.Exec(stmt); err != nil {
			return fmt.Errorf("init metadata schema: %w", err)
		}
	}

	return nil
}

// Upsert inserts or updates a session metadata record
func (r *SQLiteMetadataRepository) Upsert(ctx context.Context, meta Metadata) error {
	row := newMetadataRow(meta)
	query := `
		INSERT INTO sessions (
			id, title, created_at, updated_at, workspace_dir, provider, model,
			client_type, client_identity, active, temporary
		) VALUES (:id, :title, :created_at, :updated_at, :workspace_dir, :provider, :model, :client_type, :client_identity, :active, :temporary)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title,
			created_at=excluded.created_at,
			updated_at=excluded.updated_at,
			workspace_dir=excluded.workspace_dir,
			provider=excluded.provider,
			model=excluded.model,
			client_type=excluded.client_type,
			client_identity=excluded.client_identity,
			active=excluded.active,
			temporary=excluded.temporary;
	`
	_, err := r.db.NamedExecContext(ctx, query, row)
	if err != nil {
		return fmt.Errorf("upsert metadata: %w", err)
	}
	return nil
}

func (r *SQLiteMetadataRepository) GetByID(ctx context.Context, id string) (*Metadata, error) {
	query := `
		SELECT id, title, created_at, updated_at, workspace_dir, provider, model,
			client_type, client_identity, active, temporary
		FROM sessions
		WHERE id = ?;
	`
	var row metadataRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get metadata by id: %w", err)
	}

	return row.toMetadata()
}

func (r *SQLiteMetadataRepository) ListAll(ctx context.Context) ([]Metadata, error) {
	query := `
		SELECT id, title, created_at, updated_at, workspace_dir, provider, model,
			client_type, client_identity, active, temporary
		FROM sessions
		WHERE temporary = 0
		ORDER BY updated_at DESC;
    `
	var rows []*metadataRow
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("list metadata: %w", err)
	}

	result := make([]Metadata, 0, len(rows))
	for _, row := range rows {
		meta, err := row.toMetadata()
		if err != nil {
			return nil, fmt.Errorf("decode metadata row: %w", err)
		}
		result = append(result, *meta)
	}

	return result, nil
}

func (r *SQLiteMetadataRepository) GetActiveForClient(ctx context.Context, clientType, clientIdentity string) (*Metadata, error) {
	query := `
		SELECT id, title, created_at, updated_at, workspace_dir, provider, model,
			client_type, client_identity, active, temporary
		FROM sessions
		WHERE client_type = ? AND client_identity = ? AND active = 1 AND temporary = 0
		ORDER BY updated_at DESC
		LIMIT 1;
	`

	var row metadataRow
	if err := r.db.GetContext(ctx, &row, query, clientType, clientIdentity); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get active session metadata: %w", err)
	}

	return row.toMetadata()
}

func (r *SQLiteMetadataRepository) DeactivateClientSessions(ctx context.Context, clientType, clientIdentity string) error {
	if clientType == "" || clientIdentity == "" {
		return nil
	}

	params := deactivateClientSessionsParams{
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		ClientType:     clientType,
		ClientIdentity: clientIdentity,
	}

	query := `
		UPDATE sessions
		SET active = 0, updated_at = :updated_at
		WHERE client_type = :client_type AND client_identity = :client_identity AND active = 1 AND temporary = 0;
	`

	if _, err := r.db.NamedExecContext(ctx, query, params); err != nil {
		return fmt.Errorf("deactivate client sessions: %w", err)
	}

	return nil
}

func (r *SQLiteMetadataRepository) SetSessionActive(ctx context.Context, sessionID string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set active tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var target activationTargetRow
	if err := tx.GetContext(ctx, &target, "SELECT client_type, client_identity, temporary FROM sessions WHERE id = ?;", sessionID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("session %s not found", sessionID)
		}
		return fmt.Errorf("load session metadata for activation: %w", err)
	}
	if target.Temporary == 1 {
		return fmt.Errorf("cannot activate temporary session %s", sessionID)
	}

	params := activateSessionParams{
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		ClientType:     target.ClientType,
		ClientIdentity: target.ClientIdentity,
		SessionID:      sessionID,
	}

	deactivateSessionsQuery := `
		UPDATE sessions
		SET active = 0, updated_at = :updated_at
		WHERE client_type = :client_type AND client_identity = :client_identity AND temporary = 0 AND active = 1;`

	activateSessionQuery := `
		UPDATE sessions
		SET active = 1, updated_at = :updated_at
		WHERE id = :session_id;`

	_, err = tx.NamedExecContext(ctx, deactivateSessionsQuery, params)
	if err != nil {
		return fmt.Errorf("deactivate sessions in tx: %w", err)
	}

	_, err = tx.NamedExecContext(ctx, activateSessionQuery, params)
	if err != nil {
		return fmt.Errorf("activate session in tx: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set active tx: %w", err)
	}

	return nil
}

// Delete removes session metadata by id.
func (r *SQLiteMetadataRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?;", id)
	if err != nil {
		return fmt.Errorf("delete session metadata: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete session metadata rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("session %s not found", id)
	}
	return nil
}

func newMetadataRow(meta Metadata) metadataRow {
	return metadataRow{
		ID:             meta.ID,
		Title:          meta.Title,
		CreatedAt:      meta.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:      meta.UpdatedAt.UTC().Format(time.RFC3339Nano),
		WorkspaceDir:   meta.WorkspaceDir,
		Provider:       meta.Provider,
		Model:          meta.Model,
		ClientType:     meta.ClientType,
		ClientIdentity: meta.ClientIdentity,
		Active:         boolToInt(meta.Active),
		Temporary:      boolToInt(meta.Temporary),
	}
}

func (r metadataRow) toMetadata() (*Metadata, error) {
	created, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	meta := &Metadata{
		ID:             r.ID,
		Title:          r.Title,
		CreatedAt:      created,
		UpdatedAt:      updated,
		WorkspaceDir:   r.WorkspaceDir,
		Provider:       r.Provider,
		Model:          r.Model,
		ClientType:     r.ClientType,
		ClientIdentity: r.ClientIdentity,
		Active:         r.Active == 1,
		Temporary:      r.Temporary == 1,
	}
	return meta, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
